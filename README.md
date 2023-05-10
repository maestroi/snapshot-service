# Docker Container Snapshot Service

The Docker Container Snapshot Service is a simple utility that automates the process of stopping a Docker container, creating a tar.gz archive of a specified folder, uploading the archive to an S3-compatible storage service (e.g., AWS S3 or Wasabi), and restarting the container. The backup process is scheduled to run daily.

## Prerequisites

- Go 1.16 or later
- Docker installed and running on the host machine

## Dependencies

- [github.com/aws/aws-sdk-go](https://github.com/aws/aws-sdk-go)
- [github.com/docker/docker](https://github.com/docker/docker)
- [github.com/robfig/cron/v3](https://github.com/robfig/cron)

## Installation

1. Clone the repository:

```bash
git clone https://github.com/maestroi/snapshot-service.git
```

2. Change the working directory:

```bash
cd snapshot-service
```

3. Install the dependencies:

```bash
go get -d ./...
```

4. Build the binary:

```bash
go build -o snapshot-service ./cmd/main.go
```

## Configuration

Create a `config.json` file  with the following structure:

```json
{
    "container_name": "your_container_name",
    "file_path": "path/to/your/folder",
    "bucket_name": "your_s3_bucket_name",
    "key_name": "your_object_key_name",
    "access_key": "your_s3_access_key",
    "secret_key": "your_s3_secret_key",
    "endpoint": "https://your_s3_endpoint",
    "region": "your_s3_region"
}
```

Replace the placeholders with your actual values:

- `container_name`: The name of the Docker container you want to stop and restart.
- `file_path`: The path to the folder you want to archive and upload to S3.
- `bucket_name`: The name of the S3 bucket where the archive will be uploaded.
- `key_name`: The S3 object key (path) where the archive will be stored.
- `access_key`: Your S3-compatible storage service access key.
- `secret_key`: Your S3-compatible storage service secret key.
- `endpoint`: The endpoint URL of your S3-compatible storage service.
- `region`: The region of your S3-compatible storage service.

## Usage

Start the Docker Container Snapshot Service with the following command:

```bash
./snapshot-service -config config.json
```

The service will read the configuration from the specified file and start the backup process every 24 hours.

## License

This project is licensed under the MIT License. See the [LICENSE](LICENSE) file for details.
