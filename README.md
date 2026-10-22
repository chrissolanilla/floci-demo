# One Image

A simple Vue frontend and Go backend where users can create an account, log in, and upload an image.

The point is to demonstrate how [Floci](https://floci.io/floci/) can be used to test AWS deployments locally without using real AWS resources.

`start.sh` starts Floci and runs `provision.go`, which creates the local S3, RDS, EC2, and EBS resources. The rest is just a normal Go web app.

## Run locally

```bash
cd backend
go run .
```

In another terminal:

```bash
cd frontend
npm run dev
```

## Run with Floci

Requires Docker, AWS CLI, Go, and [Floci CLI](https://github.com/floci-io/floci-cli)

From the project root:

```bash
bash start.sh
```

This starts Floci, provisions the AWS resources, and runs the app through Docker Compose.

The app uses S3 for images and RDS/PostgreSQL for accounts and sessions. EC2 and EBS are also provisioned to demonstrate those services, but the app itself runs in Docker.

All configuration is in the root `.env` file.

To stop:

```bash
docker compose down
```
