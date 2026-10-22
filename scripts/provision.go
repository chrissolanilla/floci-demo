package main

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"strings"
)

func setting(key string) string {
	value := os.Getenv(key)
	if value == "" {
		log.Fatalf("missing %s; check the root .env", key)
	}
	return value
}

//run an aws command using our local floci endpoint
func aws(service string, args ...string) string {
	options := []string{
		"--endpoint-url", setting("AWS_ENDPOINT_URL"),
		"--region", setting("AWS_REGION"),
		service,
	}

	options = append(options, args...)

	command := exec.Command("aws", options...)
	command.Stderr = os.Stderr

	output, err := command.Output()
	if err != nil {
		log.Fatalf("aws %s failed: %v", service, err)
	}

	return strings.TrimSpace(string(output))
}

//just wrappers so we know which service we are using
func s3(args ...string) string {
	return aws("s3api", args...)
}

func rds(args ...string) string {
	return aws("rds", args...)
}

func ec2(args ...string) string {
	return aws("ec2", args...)
}

//create our bucket if it doesnt exist already
func provisionBucket() {
	bucket := setting("S3_BUCKET")
	region := setting("AWS_REGION")

	existing := s3(
		"list-buckets",
		"--query", fmt.Sprintf("Buckets[?Name=='%s'].Name", bucket),
		"--output", "text",
	)

	if existing != "" {
		log.Printf("S3 bucket already exists: %s", bucket)
		return
	}

	args := []string{
		"create-bucket",
		"--bucket", bucket,
	}

	//us-east-1 doesnt need a location constraint
	if region != "us-east-1" {
		args = append(args,
			"--create-bucket-configuration",
			"LocationConstraint="+region,
		)
	}

	s3(args...)
	log.Printf("created S3 bucket: %s", bucket)
}

//create our postgres database if it doesnt exist already
func provisionDatabase() {
	instance := setting("DB_INSTANCE")
	port := setting("DB_PORT")

	existing := rds(
		"describe-db-instances",
		"--query", fmt.Sprintf(
			"DBInstances[?DBInstanceIdentifier=='%s'].DBInstanceIdentifier",
			instance,
		),
		"--output", "text",
	)

	if existing == "" {
		rds(
			"create-db-instance",
			"--db-instance-identifier", instance,
			"--db-instance-class", "db.t3.micro",
			"--engine", "postgres",
			"--engine-version", "16",
			"--master-username", setting("DB_USER"),
			"--master-user-password", setting("DB_PASSWORD"),
			"--db-name", setting("DB_NAME"),
			"--port", port,
			"--allocated-storage", "20",
		)

		log.Printf("created RDS instance: %s", instance)
	} else {
		log.Printf("RDS instance already exists: %s", instance)
	}

	//wait until the database is available before starting the app
	rds(
		"wait", "db-instance-available",
		"--db-instance-identifier", instance,
	)

	actualPort := rds(
		"describe-db-instances",
		"--db-instance-identifier", instance,
		"--query", "DBInstances[0].Endpoint.Port",
		"--output", "text",
	)

	if actualPort != port {
		log.Fatalf("RDS is using port %s; update DB_PORT in .env", actualPort)
	}

	log.Printf("RDS ready: %s on port %s", instance, actualPort)
}

//create or start our demo ec2 instance
func provisionInstance() string {
	instance := ec2(
		"describe-instances",
		"--filters",
		"Name=tag:Name,Values=floci-demo-app",
		"Name=instance-state-name,Values=pending,running,stopped",
		"--query", "Reservations[].Instances[].InstanceId",
		"--output", "text",
	)

	if instance == "" {
		instance = ec2(
			"run-instances",
			"--image-id", "ami-alpine",
			"--instance-type", "t3.micro",
			"--count", "1",
			"--tag-specifications",
			"ResourceType=instance,Tags=[{Key=Name,Value=floci-demo-app}]",
			"--query", "Instances[0].InstanceId",
			"--output", "text",
		)

		log.Printf("created EC2 instance: %s", instance)
	} else {
		log.Printf("EC2 instance already exists: %s", instance)
	}

	state := ec2(
		"describe-instances",
		"--instance-ids", instance,
		"--query", "Reservations[0].Instances[0].State.Name",
		"--output", "text",
	)

	if state == "stopped" {
		ec2("start-instances", "--instance-ids", instance)
	}

	//wait until our instance is running
	ec2(
		"wait", "instance-running",
		"--instance-ids", instance,
	)

	log.Printf("EC2 ready: %s", instance)
	return instance
}

//create an ebs disk and attach it to our ec2 instance
func provisionVolume(instance string) string {
	zone := ec2(
		"describe-instances",
		"--instance-ids", instance,
		"--query", "Reservations[0].Instances[0].Placement.AvailabilityZone",
		"--output", "text",
	)

	volume := ec2(
		"describe-volumes",
		"--filters", "Name=tag:Name,Values=floci-demo-disk",
		"--query", "Volumes[].VolumeId",
		"--output", "text",
	)

	if volume == "" {
		volume = ec2(
			"create-volume",
			"--availability-zone", zone,
			"--size", "1",
			"--volume-type", "gp3",
			"--tag-specifications",
			"ResourceType=volume,Tags=[{Key=Name,Value=floci-demo-disk}]",
			"--query", "VolumeId",
			"--output", "text",
		)

		log.Printf("created EBS volume: %s", volume)
	} else {
		log.Printf("EBS volume already exists: %s", volume)
	}

	//check if the disk is already attached to an ec2 instance
	attached := ec2(
		"describe-volumes",
		"--volume-ids", volume,
		"--query", "Volumes[].Attachments[].InstanceId",
		"--output", "text",
	)

	if attached == "" {
		//wait for the disk to become available before attaching it
		ec2(
			"wait", "volume-available",
			"--volume-ids", volume,
		)

		ec2(
			"attach-volume",
			"--volume-id", volume,
			"--instance-id", instance,
			"--device", "/dev/sdf",
		)

		log.Printf("attached EBS %s to EC2 %s", volume, instance)
	} else if attached != instance {
		log.Fatalf("EBS volume is attached to another instance: %s", attached)
	}

	return volume
}

func main() {
	//we dont need the datetime in logs
	log.SetFlags(0)
	//only allow local floci endpoints, never real AWS
	endpoint := setting("AWS_ENDPOINT_URL")
	u, err := url.Parse(endpoint)

	if err != nil || u.Scheme != "http" || u.User != nil ||
		(u.Hostname() != "localhost" &&
			u.Hostname() != "127.0.0.1" &&
			u.Hostname() != "localhost.floci.io") {
		log.Fatal("set a local AWS_ENDPOINT_URL first: eval \"$(floci env)\"")
	}
	//disable the aws cli pager
	os.Setenv("AWS_PAGER", "")

	provisionBucket()
	provisionDatabase()
	instance := provisionInstance()
	volume := provisionVolume(instance)

	log.Printf("provisioning complete: EC2 %s, EBS %s", instance, volume)
}
