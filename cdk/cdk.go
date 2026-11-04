package main

import (
	"log"
	"os"

	"github.com/aws/aws-cdk-go/awscdk/v2"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsec2"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsrds"
	"github.com/aws/aws-cdk-go/awscdk/v2/awss3"
	"github.com/aws/constructs-go/constructs/v10"
	"github.com/aws/jsii-runtime-go"
)

// amzon linux 2023 ami
const amiString = "ami-0abcdef1234567891"

func setting(key string) *string {
	value := os.Getenv(key)

	if value == "" {
		log.Fatalf("missing environment variable: %s", key)
	}

	return jsii.String(value)
}

// all of the create functions are more so for readabiltiy, make the main function smaller
func createBucket(awsStack awscdk.Stack) awss3.Bucket {
	return awss3.NewBucket(
		awsStack,
		jsii.String("ImagesBucket"),
		&awss3.BucketProps{
			BucketName:        setting("S3_BUCKET"),
			BlockPublicAccess: awss3.BlockPublicAccess_BLOCK_ALL(),
			RemovalPolicy:     awscdk.RemovalPolicy_RETAIN,
		},
	)
}

func createVpc(awsStack awscdk.Stack) awsec2.Vpc {
	return awsec2.NewVpc(
		awsStack,
		jsii.String("AppVpc"),
		&awsec2.VpcProps{
			MaxAzs:                       jsii.Number(2),
			NatGateways:                  jsii.Number(0),
			RestrictDefaultSecurityGroup: jsii.Bool(false),
			SubnetConfiguration: &[]*awsec2.SubnetConfiguration{
				{
					Name:       jsii.String("Public"),
					SubnetType: awsec2.SubnetType_PUBLIC,
					CidrMask:   jsii.Number(24),
				},
			},
		},
	)
}

func createSecurityGroups(
	awsStack awscdk.Stack,
	vpc awsec2.Vpc,
) (awsec2.SecurityGroup, awsec2.SecurityGroup) {

	ec2SecurityGroup := awsec2.NewSecurityGroup(
		awsStack,
		jsii.String("AppSecurityGroup"),
		&awsec2.SecurityGroupProps{
			Vpc:              vpc,
			AllowAllOutbound: jsii.Bool(true),
		},
	)

	ec2SecurityGroup.AddIngressRule(
		awsec2.Peer_Ipv4(jsii.String("0.0.0.0/0")),
		awsec2.Port_Tcp(jsii.Number(8080)),
		jsii.String("Allow application HTTP"),
		jsii.Bool(false),
	)

	rdsSecurityGroup := awsec2.NewSecurityGroup(
		awsStack,
		jsii.String("DatabaseSecurityGroup"),
		&awsec2.SecurityGroupProps{
			Vpc: vpc,
		},
	)

	rdsSecurityGroup.AddIngressRule(
		ec2SecurityGroup,
		awsec2.Port_Tcp(jsii.Number(5432)),
		jsii.String("Allow PostgreSQL from application"),
		jsii.Bool(false),
	)

	return ec2SecurityGroup, rdsSecurityGroup
}

func createEC2(
	awsStack awscdk.Stack,
	vpc awsec2.Vpc,
	ec2SecurityGroup awsec2.SecurityGroup,
) awsec2.Instance {

	instance := awsec2.NewInstance(
		awsStack,
		jsii.String("AppInstance"),
		&awsec2.InstanceProps{
			Vpc: vpc,

			VpcSubnets: &awsec2.SubnetSelection{
				SubnetType: awsec2.SubnetType_PUBLIC,
			},

			InstanceType: awsec2.InstanceType_Of(
				awsec2.InstanceClass_T3,
				awsec2.InstanceSize_MICRO,
			),

			MachineImage: awsec2.MachineImage_GenericLinux(
				&map[string]*string{
					"us-east-1": jsii.String(amiString),
				},
				nil,
			),

			SecurityGroup: ec2SecurityGroup,
		},
	)

	awscdk.Tags_Of(instance).Add(
		jsii.String("Name"),
		jsii.String("floci-demo-app"),
		nil,
	)

	return instance
}

func createEBS(
	awsStack awscdk.Stack,
	instance awsec2.Instance,
) awsec2.CfnVolume {

	ebsVolume := awsec2.NewCfnVolume(
		awsStack,
		jsii.String("AppVolume"),
		&awsec2.CfnVolumeProps{
			AvailabilityZone: instance.InstanceAvailabilityZone(),
			Size:             jsii.Number(1),
			VolumeType:       jsii.String("gp3"),
		},
	)

	awsec2.NewCfnVolumeAttachment(
		awsStack,
		jsii.String("VolumeAttachment"),
		&awsec2.CfnVolumeAttachmentProps{
			InstanceId: instance.InstanceId(),
			VolumeId:   ebsVolume.Ref(),
			Device:     jsii.String("/dev/sdf"),
		},
	)

	return ebsVolume
}

func createDatabase(
	awsStack awscdk.Stack,
	vpc awsec2.Vpc,
	rdsSecurityGroup awsec2.SecurityGroup,
) awsrds.CfnDBInstance {

	rdsSubnets := vpc.PublicSubnets()
	subnetIDs := make([]interface{}, 0, len(*rdsSubnets))

	for _, subnet := range *rdsSubnets {
		subnetIDs = append(subnetIDs, subnet.SubnetId())
	}

	subnetGroup := awsrds.NewCfnDBSubnetGroup(
		awsStack,
		jsii.String("DatabaseSubnetGroup"),
		&awsrds.CfnDBSubnetGroupProps{
			DbSubnetGroupDescription: jsii.String(
				"Subnets for Floci PostgreSQL",
			),
			SubnetIds: &subnetIDs,
		},
	)

	return awsrds.NewCfnDBInstance(
		awsStack,
		jsii.String("Database"),
		&awsrds.CfnDBInstanceProps{
			DbInstanceIdentifier: setting("DB_INSTANCE"),
			DbInstanceClass:      jsii.String("db.t3.micro"),

			Engine:        jsii.String("postgres"),
			EngineVersion: jsii.String("16"),

			MasterUsername: setting("DB_USER"),
			//PROBABLY DONT DO THIS IN ACTUAL IMPORTANT ENVS but this is just a ddemo
			MasterUserPassword: setting("DB_PASSWORD"),

			DbName: setting("DB_NAME"),

			Port: jsii.String("5432"),

			AllocatedStorage: jsii.String("20"),

			DbSubnetGroupName: subnetGroup.Ref(),

			VpcSecurityGroups: &[]interface{}{
				rdsSecurityGroup.SecurityGroupId(),
			},
		},
	)
}

// THIS is so that start.sh can read the outputs and emulate
func createOutputs(
	awsStack awscdk.Stack,
	bucket awss3.Bucket,
	instance awsec2.Instance,
	database awsrds.CfnDBInstance,
	ebsVolume awsec2.CfnVolume,
) {

	awscdk.NewCfnOutput(
		awsStack,
		jsii.String("BucketName"),
		&awscdk.CfnOutputProps{
			Value: bucket.BucketName(),
		},
	)

	awscdk.NewCfnOutput(
		awsStack,
		jsii.String("InstanceId"),
		&awscdk.CfnOutputProps{
			Value: instance.InstanceId(),
		},
	)

	awscdk.NewCfnOutput(
		awsStack,
		jsii.String("DatabaseEndpoint"),
		&awscdk.CfnOutputProps{
			Value: database.AttrEndpointAddress(),
		},
	)

	awscdk.NewCfnOutput(
		awsStack,
		jsii.String("VolumeId"),
		&awscdk.CfnOutputProps{
			Value: ebsVolume.Ref(),
		},
	)
}

func NewCdkStack(
	scope constructs.Construct,
	id string,
	props *awscdk.StackProps,
) awscdk.Stack {
	awsStack := awscdk.NewStack(scope, &id, props)
	bucket := createBucket(awsStack)
	vpc := createVpc(awsStack)

	ec2SecurityGroup, rdsSecurityGroup := createSecurityGroups(awsStack, vpc)
	instance := createEC2(awsStack, vpc, ec2SecurityGroup)
	ebsVolume := createEBS(awsStack, instance)

	database := createDatabase(awsStack, vpc, rdsSecurityGroup)

	createOutputs(awsStack, bucket, instance, database, ebsVolume)

	return awsStack
}

func main() {
	//jsii allows other langauges to talk to aws typescript cdk setup
	//we need to remeember to close this when program is over(defer amkes it execute last)
	defer jsii.Close()

	app := awscdk.NewApp(nil)
	environment := &awscdk.Environment{
		//12 0's are the default local account for floci
		Account: jsii.String("000000000000"),
		// Account: jsii.String("696969696969"),
		Region: jsii.String("us-east-1"),
	}

	stackProps := &awscdk.StackProps{
		Env:         environment,
		Synthesizer: awscdk.NewBootstraplessSynthesizer(nil),
	}

	NewCdkStack(app, "FlociDemoStack", stackProps)

	app.Synth(nil)
}
