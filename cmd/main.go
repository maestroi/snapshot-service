package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/robfig/cron/v3"
	"golang.org/x/net/context"
)

type Config struct {
	ContainerName string `json:"container_name"`
	FilePath      string `json:"file_path"`
	BucketName    string `json:"bucket_name"`
	AccessKey     string `json:"access_key"`
	SecretKey     string `json:"secret_key"`
	Endpoint      string `json:"endpoint"`
	Region        string `json:"region"`
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
		config = loadConfigFromEnv()
	}
}

func getDockerClient() (*client.Client, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	return cli, nil
}

func loadConfigFromEnv() *Config {
	return &Config{
		ContainerName: os.Getenv("CONTAINER_NAME"),
		FilePath:      os.Getenv("FILE_PATH"),
		BucketName:    os.Getenv("BUCKET_NAME"),
	}
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

	timeout := int(30)
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

func createTarGz(folderPath string, archivePath string) error {
	file, err := os.Create(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()

	gw := gzip.NewWriter(file)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	err = filepath.Walk(folderPath, func(path string, info os.FileInfo, err error) error {
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

	return err
}
func main() {
	log.Println("Nimiq snapshot Genrator started")
	log.Println("Set crontime to: @daily")

	c := cron.New(cron.WithSeconds()) // Use cron.WithSeconds() if you want to schedule tasks with second-level precision
	c.AddFunc("@daily", runBackupProcess)

	c.Start()

	// Keep the main function running indefinitely
	select {}
}

func runBackupProcess() {

	log.Printf("Stopping container %s", config.ContainerName)
	err := stopContainer(config.ContainerName)
	if err != nil {
		log.Fatalf("Error stopping container %s: %v", config.ContainerName, err)
	} else {
		fmt.Printf("Container %s stopped\n", config.ContainerName)
	}

	currentDateTime := time.Now().Format("20060102-150405")
	archivePath := fmt.Sprintf("%s.tar.gz", currentDateTime)

	log.Println("Creating tar.gz archive")
	err = createTarGz(config.FilePath, archivePath)
	if err != nil {
		log.Fatalf("Error creating tar.gz archive: %v", err)
	}

	// Upload the tar.gz archive to S3
	log.Println("Uploading to S3 into bucket", config.BucketName)
	err = uploadToS3(archivePath, config.BucketName, fmt.Sprintf("%s/%s", currentDateTime, filepath.Base(archivePath)))
	if err != nil {
		log.Fatalf("Error uploading to S3: %v", err)
	}

	// Remove the tar.gz archive after the upload is complete
	log.Println("Removing tar.gz archive")
	err = os.Remove(archivePath)
	if err != nil {
		log.Fatalf("Error removing tar.gz archive: %v", err)
	}

	log.Printf("Starting container %s", config.ContainerName)
	err = startContainerByName(config.ContainerName)
	if err != nil {
		log.Fatalf("Error starting container %s: %v", config.ContainerName, err)
	} else {
		fmt.Printf("Container %s started\n", config.ContainerName)
	}
	log.Println("Nimiq snapshot Genrator finished")
}
