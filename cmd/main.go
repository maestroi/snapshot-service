package main

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
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

const (
	appVersion = "1.0.0"
)

type Config struct {
	ContainerNames  []string `json:"container_names"`
	Network         string   `json:"network"`
	Protocol        string   `json:"protocol"`
	ProtocolHistory string   `json:"protocol_history"`
	ProtocolVer     string   `json:"protocol_version"`
	IgnoreFiles     []string `json:"ignore_files"`
	CrontTime       string   `json:"cron_time"`
	FilePath        string   `json:"file_path"`
	BucketName      string   `json:"bucket_name"`
	AccessKey       string   `json:"access_key"`
	SecretKey       string   `json:"secret_key"`
	Endpoint        string   `json:"endpoint"`
	Region          string   `json:"region"`
	SnapshotToKeep  int      `json:"snapshot_to_keep"`
}

type SnapshotStatus struct {
	DateTime        string `json:"dateTime"`
	FileName        string `json:"fileName"`
	Status          string `json:"status"`
	Network         string `json:"network"`
	Protocol        string `json:"protocol"`
	ProtocolVersion string `json:"protocolVersion"`
}

type Metadata struct {
	DateTime         string `json:"dateTime"`
	FileName         string `json:"fileName"`
	Network          string `json:"network"`
	Protocol         string `json:"protocol"`
	ProtocolHistory  string `json:"protocolHistory"`
	ProtocolVersion  string `json:"protocolVersion"`
	SnapshotVersion  string `json:"snapshotVersion"`
	BlockHash        string `json:"blockHash"`
	BlockHeight      string `json:"blockHeight"`
	UncommpresedSize int64  `json:"uncommpresedSize"`
	DataDirSha256    string `json:"dataDirSha256"`
	Status           string `json:"status"`
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

func calculateNextRun() {
	if config.CrontTime == "direct" {
		log.Println("Timer: Direct run")
		return
	}

	sched, err := cron.ParseStandard(config.CrontTime)
	if err != nil {
		log.Fatalf("Error parsing cron time: %v", err)
	}

	now := time.Now()
	nextRun := sched.Next(now)

	log.Println("Timer: Next run", nextRun.Format("2006-01-02 15:04:05"))
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
	logPrefix := "PruneOldSnapshots: "
	directoryPrefix := fmt.Sprintf("%s/%s/", config.Protocol, config.Network)
	fileNameSuffixes := []string{".tar.gz", "-metadata.json"}

	svc := s3.New(sess)

	resp, err := svc.ListObjectsV2(&s3.ListObjectsV2Input{Bucket: aws.String(bucketName), Prefix: aws.String(directoryPrefix)})
	if err != nil {
		return err
	}

	type fileWithTimestamp struct {
		key       string
		timestamp time.Time
	}
	tarFiles := []fileWithTimestamp{}
	jsonFiles := []fileWithTimestamp{}

	log.Printf("%sLooking for old snapshot files in bucket %s", logPrefix, bucketName)
	for _, item := range resp.Contents {
		key := *item.Key
		for _, suffix := range fileNameSuffixes {
			if strings.HasSuffix(key, suffix) {
				timestampStr := strings.TrimSuffix(strings.TrimPrefix(key, directoryPrefix), suffix)
				timestamp, err := time.Parse("20060102-150405", timestampStr)
				if err != nil {
					return err
				}
				file := fileWithTimestamp{key: key, timestamp: timestamp}
				if suffix == ".tar.gz" {
					tarFiles = append(tarFiles, file)
				} else if suffix == "-metadata.json" {
					jsonFiles = append(jsonFiles, file)
				}
				break
			}
		}
	}

	// Sort files by timestamp
	sort.Slice(tarFiles, func(i, j int) bool {
		return tarFiles[i].timestamp.Before(tarFiles[j].timestamp)
	})
	sort.Slice(jsonFiles, func(i, j int) bool {
		return jsonFiles[i].timestamp.Before(jsonFiles[j].timestamp)
	})

	// Delete old .tar.gz files
	if len(tarFiles) > config.SnapshotToKeep {
		log.Printf("%sFound %d .tar.gz files in bucket %s, deleting older ones", logPrefix, len(tarFiles), bucketName)
		for _, file := range tarFiles[:len(tarFiles)-config.SnapshotToKeep] {
			log.Printf("%sDeleting .tar.gz file %s", logPrefix, file.key)
			_, err := svc.DeleteObject(&s3.DeleteObjectInput{Bucket: aws.String(bucketName), Key: aws.String(file.key)})
			if err != nil {
				return err
			}
		}
	} else {
		log.Printf("%sFound %d .tar.gz files in bucket %s, nothing to delete", logPrefix, len(tarFiles), bucketName)
	}

	// Delete old -metadata.json files
	if len(jsonFiles) > config.SnapshotToKeep {
		log.Printf("%sFound %d -metadata.json files in bucket %s, deleting older ones", logPrefix, len(jsonFiles), bucketName)
		for _, file := range jsonFiles[:len(jsonFiles)-config.SnapshotToKeep] {
			log.Printf("%sDeleting -metadata.json file %s", logPrefix, file.key)
			_, err := svc.DeleteObject(&s3.DeleteObjectInput{Bucket: aws.String(bucketName), Key: aws.String(file.key)})
			if err != nil {
				return err
			}
		}
	} else {
		log.Printf("%sFound %d -metadata.json files in bucket %s, nothing to delete", logPrefix, len(jsonFiles), bucketName)
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

func stopContainers(containerNames []string) error {
	for _, containerName := range containerNames {
		if err := stopContainer(containerName); err != nil {
			log.Printf("ContainerService: Error stopping container %s: %v", containerName, err)
			return err
		}
		log.Printf("ContainerService: Container %s stopped\n", containerName)
	}
	return nil
}

func startContainers(containerNames []string) error {
	for _, containerName := range containerNames {
		if err := startContainerByName(containerName); err != nil {
			log.Printf("ContainerService: Error starting container %s: %v", containerName, err)
			return err
		}
		log.Printf("ContainerService: Container %s started\n", containerName)
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

func CalculateDirectorySize(path string) (int64, error) {
	var size int64
	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if !info.IsDir() {
			size += info.Size()
		}
		return err
	})
	return size, err
}

func createTarGzToS3(bucketName string, key string, folderPath string) error {
	log.Println("ArchiveCreate: Create and Stream snapshot to S3")
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

			// If the current file is in the ignore list, skip it
			for _, ignore := range config.IgnoreFiles {
				if filepath.Base(path) == ignore {
					log.Printf("Skipping %s", path)
					return nil
				}
			}

			if info.IsDir() {
				return nil
			}

			relPath, err := filepath.Rel(folderPath, path)
			if err != nil {
				return err
			}

			log.Printf("ArchiveCreate: Adding %s", relPath)

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
			return
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
	log.Printf("Snapshot service started. Protocol: %s, Network: %s, Crontime: %s",
		config.Protocol, config.Network, config.CrontTime)

	if config.CrontTime == "direct" {
		log.Println("StartSnapshot: Direct start")
		if err := runBackupProcess(); err != nil {
			log.Printf("Error running backup process: %v", err)
			return
		}
		return
	}

	calculateNextRun()
	c := cron.New()
	_, err := c.AddFunc(config.CrontTime, func() {
		if err := runBackupProcess(); err != nil {
			log.Printf("Error running backup process: %v", err)
		}
	})
	if err != nil {
		log.Printf("Error adding function to cron: %v", err)
		return
	}
	c.Start()

	// Block the main goroutine indefinitely
	select {}
}

func WriteMetadataToFile(metadata Metadata, filename string) error {
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}

	err = ioutil.WriteFile(filename, data, 0644)
	if err != nil {
		return err
	}

	return nil
}

func hashDirectory(dir string) (string, error) {
	hash := sha256.New()

	var files []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		for _, ignore := range config.IgnoreFiles {
			if filepath.Base(path) == ignore {
				return nil
			}
		}
		if !info.IsDir() {
			files = append(files, path)
		}
		return nil
	})

	if err != nil {
		return "", err
	}

	sort.Strings(files)

	for _, file := range files {
		f, err := os.Open(file)
		if err != nil {
			return "", err
		}

		_, err = io.Copy(hash, f)
		f.Close()
		if err != nil {
			return "", err
		}
	}

	hashStr := fmt.Sprintf("%x", hash.Sum(nil))
	return hashStr, nil
}

func uploadAndCleanUp(file string, bucket string, key string) error {
	log.Println("UploadS3: Uploading state file to bucket", bucket)
	err := uploadToS3(file, bucket, key)
	if err != nil {
		log.Printf("UploadS3: Error uploading to S3: %v", err)
		return err
	}

	log.Println("CleanUp: Removing ", file)
	err = os.Remove(file)
	if err != nil {
		log.Printf("cleanUp: Error Statefile: %v", err)
		return err
	}

	return nil
}

func createSnapShotMetadata(key, time, status string) error {
	DataFile := fmt.Sprintf("%s.tar.gz", time)
	metaDataFile := fmt.Sprintf("%s-metadata.json", time)
	metaDataLatest := "snapshot-latest.json"
	metaDataFileKey := fmt.Sprintf("%s/%s", key, metaDataFile)
	metaDataLatestKey := fmt.Sprintf("%s/%s", key, metaDataLatest)
	notYetImplemented := "Unknown"

	dirSize, err := CalculateDirectorySize(config.FilePath)
	if err != nil {
		return err
	}

	hashString, err := hashDirectory(config.FilePath)
	if err != nil {
		return err
	}

	metadata := Metadata{
		DateTime:         time,
		FileName:         DataFile,
		Network:          config.Network,
		Protocol:         config.Protocol,
		ProtocolHistory:  config.ProtocolHistory,
		ProtocolVersion:  config.ProtocolVer,
		SnapshotVersion:  appVersion,
		BlockHash:        notYetImplemented,
		BlockHeight:      notYetImplemented,
		UncommpresedSize: dirSize,
		DataDirSha256:    hashString,
		Status:           status,
	}

	err = WriteMetadataToFile(metadata, metaDataFile)
	if err != nil {
		return err
	}

	err = WriteMetadataToFile(metadata, metaDataLatest)
	if err != nil {
		return err
	}

	err = uploadAndCleanUp(metaDataFile, config.BucketName, metaDataFileKey)
	if err != nil {
		return err
	}

	err = uploadAndCleanUp(metaDataLatest, config.BucketName, metaDataLatestKey)
	if err != nil {
		return err
	}

	return nil
}

func runBackupProcess() error {
	pruneOldSnapshots()
	status := "success"

	currentTime := currentDateTime()
	key := fmt.Sprintf("%s/%s", config.Protocol, config.Network)
	tarFile := fmt.Sprintf("%s/%s.tar.gz", key, currentTime)

	if err := stopContainers(config.ContainerNames); err != nil {
		return fmt.Errorf("error stopping containers: %v", err)
	}

	err := createTarGzToS3(config.BucketName, tarFile, config.FilePath)
	if err != nil {
		status = "error"
	}

	err = createSnapShotMetadata(key, currentTime, status)
	if err != nil {
		status = "error"
	}

	if err := startContainers(config.ContainerNames); err != nil {
		return fmt.Errorf("error starting containers: %v", err)
	}

	calculateNextRun()
	log.Printf("Service: %s Snapshot finished", config.Protocol)

	if status == "error" {
		return errors.New("runBackupProcess finished with errors")
	}

	return nil
}
