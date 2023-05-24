package main

import (
	"archive/tar"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/klauspost/pgzip"
	"github.com/robfig/cron/v3"
	"golang.org/x/net/context"
)

type Config struct {
	ContainerName  string `json:"container_name"`
	Network        string `json:"network"`
	Protocol       string `json:"protocol"`
	ProtocolVer    string `json:"protocol_version"`
	CrontTime      string `json:"cron_time"`
	FilePath       string `json:"file_path"`
	BucketName     string `json:"bucket_name"`
	AccessKey      string `json:"access_key"`
	SecretKey      string `json:"secret_key"`
	Endpoint       string `json:"endpoint"`
	Region         string `json:"region"`
	SnapshotToKeep int    `json:"snapshot_to_keep"`
}

type SnapshotStatus struct {
	DateTime        string `json:"dateTime"`
	FileName        string `json:"fileName"`
	Status          string `json:"status"`
	Network         string `json:"network"`
	Protocol        string `json:"protocol"`
	ProtocolVersion string `json:"protocolVersion"`
}

var config *Config

func init() {
	var configFilePath string
	flag.StringVar(&configFilePath, "config", "", "Path to the configuration file")
	flag.Parse()

	var err error
	if configFilePath != "" {
		config, err = loadConfig(configFilePath)
		if err != nil {
			log.Fatalf("Error loading configuration from file: %v", err)
		}
	} else {
		log.Fatalf("No configuration file provided")
	}
}

func getDockerClient() (*client.Client, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	return cli, nil
}

func loadConfig(filePath string) (*Config, error) {
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return nil, err
	}

	configFile, err := os.Open(absPath)
	if err != nil {
		return nil, err
	}
	defer configFile.Close()

	var config Config
	if err := json.NewDecoder(configFile).Decode(&config); err != nil {
		return nil, err
	}

	return &config, nil
}

func currentDateTime() string {
	return time.Now().Format("20060102-150405")
}

func pruneOldSnapshots() error {
	sess, err := session.NewSession(&aws.Config{
		Region:           aws.String(config.Region),
		Credentials:      credentials.NewStaticCredentials(config.AccessKey, config.SecretKey, ""),
		Endpoint:         aws.String(config.Endpoint),
		S3ForcePathStyle: aws.Bool(true),
	})
	if err != nil {
		return err
	}

	bucketName := config.BucketName
	logPrefix := "pruneOldSnapshots: "
	directoryPrefix := fmt.Sprintf("%s/%s/", config.Protocol, config.Network)
	fileNameSuffix := ".tar.gz"

	svc := s3.New(sess)

	resp, err := svc.ListObjectsV2(&s3.ListObjectsV2Input{Bucket: aws.String(bucketName), Prefix: aws.String(directoryPrefix)})
	if err != nil {
		return err
	}

	// Filter files based on pattern and parse their timestamps
	type fileWithTimestamp struct {
		key       string
		timestamp time.Time
	}
	files := []fileWithTimestamp{}

	log.Printf("%sLooking for old snapshot files in bucket %s", logPrefix, bucketName)
	for _, item := range resp.Contents {
		key := *item.Key
		if strings.HasSuffix(key, fileNameSuffix) {
			timestampStr := strings.TrimSuffix(strings.TrimPrefix(key, directoryPrefix), fileNameSuffix)
			timestamp, err := time.Parse("20060102-150405", timestampStr)
			if err != nil {
				return err
			}
			files = append(files, fileWithTimestamp{key: key, timestamp: timestamp})
		}
	}

	// Sort files by timestamp
	sort.Slice(files, func(i, j int) bool {
		return files[i].timestamp.Before(files[j].timestamp)
	})

	// Delete all but the last x files
	if len(files) > config.SnapshotToKeep {
		log.Printf("%sFound %d files in bucket %s, deleting older ones", logPrefix, len(files), bucketName)
		for _, file := range files[:len(files)-config.SnapshotToKeep] {
			log.Printf("%sDeleting file %s", logPrefix, file.key)
			_, err := svc.DeleteObject(&s3.DeleteObjectInput{Bucket: aws.String(bucketName), Key: aws.String(file.key)})
			if err != nil {
				return err
			}
		}
	} else {
		log.Printf("%sFound %d files in bucket %s, nothing to delete", logPrefix, len(files), bucketName)
	}

	return nil
}

func generateSnapshotStatus(status string, filename string) error {
	currentDateTime := currentDateTime()

	snapshotStatus := SnapshotStatus{
		DateTime:        currentDateTime,
		Protocol:        config.Protocol,
		ProtocolVersion: config.ProtocolVer,
		Network:         config.Network,
		FileName:        filename,
		Status:          status,
	}

	jsonData, err := json.MarshalIndent(snapshotStatus, "", "  ")
	if err != nil {
		return err
	}

	err = os.WriteFile("snapshot-latest.json", jsonData, 0644)
	if err != nil {
		return err
	}

	return nil
}

func getContainerID(containerName string) (string, error) {
	cli, err := getDockerClient()
	if err != nil {
		return "", err
	}
	defer cli.Close()

	containers, err := cli.ContainerList(context.Background(), types.ContainerListOptions{All: true})
	if err != nil {
		return "", err
	}

	for _, container := range containers {
		for _, name := range container.Names {
			if name == "/"+containerName {
				return container.ID, nil
			}
		}
	}

	return "", fmt.Errorf("container with name %s not found", containerName)
}

func stopContainer(containerName string) error {
	containerID, err := getContainerID(containerName)
	if err != nil {
		return err
	}

	cli, err := getDockerClient()
	if err != nil {
		return err
	}
	defer cli.Close()

	timeout := int(10)
	stopOptions := container.StopOptions{
		Timeout: &timeout,
	}
	if err := cli.ContainerStop(context.Background(), containerID, stopOptions); err != nil {
		return err
	}
	return nil
}

func startContainerByName(containerName string) error {
	containerID, err := getContainerID(containerName)
	if err != nil {
		return err
	}

	cli, err := getDockerClient()
	if err != nil {
		return err
	}
	defer cli.Close()

	if err := cli.ContainerStart(context.Background(), containerID, types.ContainerStartOptions{}); err != nil {
		return err
	}
	return nil
}

func uploadToS3(filePath, bucket, key string) error {
	sess, err := session.NewSession(&aws.Config{
		Region:           aws.String(config.Region),
		Credentials:      credentials.NewStaticCredentials(config.AccessKey, config.SecretKey, ""),
		Endpoint:         aws.String(config.Endpoint),
		S3ForcePathStyle: aws.Bool(true),
	})
	if err != nil {
		return err
	}

	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	uploader := s3manager.NewUploader(sess)
	_, err = uploader.Upload(&s3manager.UploadInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   file,
	})

	return err
}

func createTarGzToS3(bucketName string, key string, folderPath string) error {
	sess, err := session.NewSession(&aws.Config{
		Region:           aws.String(config.Region),
		Credentials:      credentials.NewStaticCredentials(config.AccessKey, config.SecretKey, ""),
		Endpoint:         aws.String(config.Endpoint),
		S3ForcePathStyle: aws.Bool(true),
	})
	if err != nil {
		return err
	}

	uploader := s3manager.NewUploader(sess)

	pr, pw := io.Pipe()

	gw, err := pgzip.NewWriterLevel(pw, pgzip.BestSpeed)
	if err != nil {
		return err
	}

	tw := tar.NewWriter(gw)

	go func() {
		defer pw.Close()
		defer gw.Close()
		defer tw.Close()

		err := filepath.Walk(folderPath, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}

			if info.IsDir() {
				return nil
			}

			relPath, err := filepath.Rel(folderPath, path)
			if err != nil {
				return err
			}

			log.Printf("Adding %s", relPath)

			header, err := tar.FileInfoHeader(info, relPath)
			if err != nil {
				return err
			}

			header.Name = relPath

			if err := tw.WriteHeader(header); err != nil {
				return err
			}

			file, err := os.Open(path)
			if err != nil {
				return err
			}
			defer file.Close()

			_, err = io.Copy(tw, file)
			return err
		})

		if err != nil {
			// handle error
		}
	}()

	_, err = uploader.Upload(&s3manager.UploadInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(key),
		Body:   pr,
	})

	if err != nil {
		return err
	}

	return nil
}

func main() {
	log.Printf("%s snapshot Genrator started", config.Protocol)
	log.Println("Protocol: " + config.Protocol)
	log.Println("Network: " + config.Network)

	pruneOldSnapshots()
	if config.CrontTime == "direct" {
		log.Println("StartSnapshot: Direct start")
		runBackupProcess()
		return
	}

	log.Println("StartSnapshot: Crontime " + config.CrontTime)
	c := cron.New(cron.WithSeconds()) // Use cron.WithSeconds() if you want to schedule tasks with second-level precision
	c.AddFunc("25 * * * * *", runBackupProcess)
	c.Start()

	// Keep the main function running indefinitely
	select {}
}

func runBackupProcess() {
	status := "success"

	log.Printf("containerService: Stopping container %s", config.ContainerName)
	err := stopContainer(config.ContainerName)
	if err != nil {
		log.Printf("containerService: Error stopping container %s: %v", config.ContainerName, err)
	} else {
		log.Printf("containerService: Container %s stopped\n", config.ContainerName)
	}

	currentDateTime := currentDateTime()
	key := fmt.Sprintf("%s/%s/%s.tar.gz", config.Protocol, config.Network, currentDateTime)

	log.Println("archiveCreate: Create and Stream snapshot to S3")
	err = createTarGzToS3(config.BucketName, key, config.FilePath)
	if err != nil {
		log.Printf("archiveCreate: Error creating tar.gz archive: %v", err)
		status = "error"
	}

	stateFileName := "snapshot-latest.json"
	stateFileKey := fmt.Sprintf("%s/%s/%s", config.Protocol, config.Network, stateFileName)

	log.Println("stateFile: Create status file")
	err = generateSnapshotStatus(status, stateFileName)
	if err != nil {
		log.Printf("stateFile: Error creating status file: %v", err)
	}

	// Upload state file
	log.Println("uploadS3: Uploading state file to bucket", config.BucketName)
	err = uploadToS3(stateFileName, config.BucketName, stateFileKey)
	if err != nil {
		log.Printf("uploadS3: Error uploading to S3: %v", err)
		status = "error"
	}

	// Remove the tar.gz archive after the upload is complete
	log.Println("cleanUp: Removing statefile.json")
	err = os.Remove(stateFileName)
	if err != nil {
		log.Printf("cleanUp: Error removing tar.gz archive: %v", err)
	}

	log.Printf("containerService: Starting container %s", config.ContainerName)
	err = startContainerByName(config.ContainerName)
	if err != nil {
		log.Fatalf("containerService: Error starting container %s: %v", config.ContainerName, err)
	} else {
		log.Printf("containerService: Container %s started\n", config.ContainerName)
	}
	log.Printf("servivce: %s Snapshot Genrator finished", config.Protocol)
}
