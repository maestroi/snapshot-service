# Docker Container Snapshot Service

The Docker Container Snapshot Service is a powerful utility designed to streamline the process of snapshotting Docker containers. The service automatically stops a specified Docker container, generates a tar.gz archive of a targeted folder, uploads this archive to an S3-compatible storage service (e.g., AWS S3 or Wasabi), and then restarts the container. This process is automated and is scheduled to run on a daily basis.

## Prerequisites

Ensure the following prerequisites are met:

- Go 1.16 or later is installed on your system.
- Docker is installed and actively running on your host machine.

## Dependencies

This service utilizes the following dependencies:

- [github.com/aws/aws-sdk-go](https://github.com/aws/aws-sdk-go)
- [github.com/docker/docker](https://github.com/docker/docker)
- [github.com/robfig/cron/v3](https://github.com/robfig/cron)

## Installation Steps

1. Clone the repository with the following command:

```bash
git clone https://github.com/maestroi/snapshot-service.git
```

2. Navigate to the cloned repository:

```bash
cd snapshot-service
```

3. Download and install the necessary dependencies:

```bash
go get -d ./...
```

4. Compile the application:

```bash
go build -o snapshot-service ./cmd/main.go
```

## Configuration Guide

You need to create a `config.json` file with the following structure:

```json
{
    "container_name": "your_container_name",
    "network": "protocol_network",
    "protocol": "protocol_name",
    "protocol_version": "protocol_version_number",
    "file_path": "path/to/your/folder",
    "bucket_name": "your_s3_bucket_name",
    "key_name": "your_object_key_name",
    "access_key": "your_s3_access_key",
    "secret_key": "your_s3_secret_key",
    "endpoint": "https://your_s3_endpoint",
    "region": "your_s3_region",
    "snapshot_to_keep": 5
}
```

Substitute the placeholders with your specific values:

- `container_name`: The Docker container's name to be stopped and restarted.
- `network`: The network of the protocol For example testnet
- `protocol`: The name of the protocol for example nimiq-v1
- `protocol_version`: The version of the protocol running on 1.0.0
- `file_path`: The directory path that you wish to archive and upload to S3.
- `bucket_name`: The destination S3 bucket's name.
- `key_name`: The designated S3 object key (path) for the stored archive.
- `access_key`: Your S3-compatible storage service's access key.
- `secret_key`: Your S3-compatible storage service's secret key.
- `endpoint`: The endpoint URL of your S3-compatible storage service.
- `region`: Your S3-compatible storage service's region.
- `snapshot_to_keep`: Amount of snapshots to keep in bucket

## Usage Instructions

You can start the Docker Container Snapshot Service using the command below:

```bash
./snapshot-service -config config.json
```

The service will parse the provided configuration file and initiate the backup procedure every 24 hours.

## License Information

This project is distributed under the MIT License. Refer to the [LICENSE](LICENSE) file for more information.
