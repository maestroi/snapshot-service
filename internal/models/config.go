package main

type Config struct {
	ContainerName string `json:"container_name"`
	FilePath      string `json:"file_path"`
	BucketName    string `json:"bucket_name"`
	KeyName       string `json:"key_name"`
}
