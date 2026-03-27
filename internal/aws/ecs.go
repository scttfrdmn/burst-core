package aws

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/scttfrdmn/burst-core/pkg/protocol"
)

// ecsAPI is a narrow interface over the ecs.Client methods used here.
type ecsAPI interface {
	DescribeClusters(ctx context.Context, in *ecs.DescribeClustersInput, opts ...func(*ecs.Options)) (*ecs.DescribeClustersOutput, error)
	CreateCluster(ctx context.Context, in *ecs.CreateClusterInput, opts ...func(*ecs.Options)) (*ecs.CreateClusterOutput, error)
	DeleteCluster(ctx context.Context, in *ecs.DeleteClusterInput, opts ...func(*ecs.Options)) (*ecs.DeleteClusterOutput, error)
	RegisterTaskDefinition(ctx context.Context, in *ecs.RegisterTaskDefinitionInput, opts ...func(*ecs.Options)) (*ecs.RegisterTaskDefinitionOutput, error)
	DeregisterTaskDefinition(ctx context.Context, in *ecs.DeregisterTaskDefinitionInput, opts ...func(*ecs.Options)) (*ecs.DeregisterTaskDefinitionOutput, error)
	ListTaskDefinitionFamilies(ctx context.Context, in *ecs.ListTaskDefinitionFamiliesInput, opts ...func(*ecs.Options)) (*ecs.ListTaskDefinitionFamiliesOutput, error)
	ListTaskDefinitions(ctx context.Context, in *ecs.ListTaskDefinitionsInput, opts ...func(*ecs.Options)) (*ecs.ListTaskDefinitionsOutput, error)
	RunTask(ctx context.Context, in *ecs.RunTaskInput, opts ...func(*ecs.Options)) (*ecs.RunTaskOutput, error)
	StopTask(ctx context.Context, in *ecs.StopTaskInput, opts ...func(*ecs.Options)) (*ecs.StopTaskOutput, error)
	DescribeTasks(ctx context.Context, in *ecs.DescribeTasksInput, opts ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error)
}

// ECSClient wraps ECS operations used by burst-core.
type ECSClient struct {
	client  ecsAPI
	region  string
	cluster string
}

// NewECSClient creates an ECSClient from an AWS config.
func NewECSClient(cfg aws.Config) *ECSClient {
	return &ECSClient{
		client:  ecs.NewFromConfig(cfg),
		region:  cfg.Region,
		cluster: "burst-cluster",
	}
}

// CreateCluster creates the burst ECS cluster idempotently.
func (c *ECSClient) CreateCluster(ctx context.Context) error {
	out, err := c.client.DescribeClusters(ctx, &ecs.DescribeClustersInput{
		Clusters: []string{c.cluster},
		Include:  []types.ClusterField{types.ClusterFieldSettings},
	})
	if err != nil {
		return &protocol.BurstSetupError{Step: "check ECS cluster", Cause: err.Error()}
	}

	for _, cl := range out.Clusters {
		if aws.ToString(cl.ClusterName) == c.cluster &&
			aws.ToString(cl.Status) == "ACTIVE" {
			return nil
		}
	}

	_, err = c.client.CreateCluster(ctx, &ecs.CreateClusterInput{
		ClusterName:       aws.String(c.cluster),
		CapacityProviders: []string{"FARGATE", "FARGATE_SPOT"},
		DefaultCapacityProviderStrategy: []types.CapacityProviderStrategyItem{
			{CapacityProvider: aws.String("FARGATE"), Weight: 1},
		},
		Settings: []types.ClusterSetting{
			{Name: types.ClusterSettingNameContainerInsights, Value: aws.String("enabled")},
		},
		Tags: []types.Tag{
			{Key: aws.String("managed-by"), Value: aws.String("burst-core")},
		},
	})
	if err != nil {
		return &protocol.BurstSetupError{
			Step:        "create ECS cluster",
			Cause:       err.Error(),
			Remediation: "ensure your AWS identity has ecs:CreateCluster permission",
		}
	}
	return nil
}

// ClusterStatus returns the current status of the burst ECS cluster (e.g. "ACTIVE"),
// or an empty string if the cluster does not exist.
func (c *ECSClient) ClusterStatus(ctx context.Context) (string, error) {
	out, err := c.client.DescribeClusters(ctx, &ecs.DescribeClustersInput{
		Clusters: []string{c.cluster},
	})
	if err != nil {
		return "", fmt.Errorf("describing cluster %q: %w", c.cluster, err)
	}
	for _, cl := range out.Clusters {
		if aws.ToString(cl.ClusterName) == c.cluster {
			return aws.ToString(cl.Status), nil
		}
	}
	return "", nil
}

// DeleteCluster deletes the burst ECS cluster.
// Returns nil if the cluster does not exist or is already INACTIVE.
func (c *ECSClient) DeleteCluster(ctx context.Context) error {
	out, err := c.client.DescribeClusters(ctx, &ecs.DescribeClustersInput{
		Clusters: []string{c.cluster},
	})
	if err != nil {
		return fmt.Errorf("describing cluster %q: %w", c.cluster, err)
	}
	found := false
	for _, cl := range out.Clusters {
		if aws.ToString(cl.ClusterName) == c.cluster &&
			aws.ToString(cl.Status) != "INACTIVE" {
			found = true
			break
		}
	}
	if !found {
		return nil
	}

	_, err = c.client.DeleteCluster(ctx, &ecs.DeleteClusterInput{
		Cluster: aws.String(c.cluster),
	})
	if err != nil {
		var notFound *types.ClusterNotFoundException
		if errors.As(err, &notFound) {
			return nil
		}
		return fmt.Errorf("deleting cluster %q: %w", c.cluster, err)
	}
	return nil
}

// ListTaskDefinitionFamilies returns all task definition family names matching prefix.
func (c *ECSClient) ListTaskDefinitionFamilies(ctx context.Context, prefix string) ([]string, error) {
	var families []string
	var nextToken *string
	for {
		out, err := c.client.ListTaskDefinitionFamilies(ctx, &ecs.ListTaskDefinitionFamiliesInput{
			FamilyPrefix: aws.String(prefix),
			NextToken:    nextToken,
		})
		if err != nil {
			return nil, fmt.Errorf("listing task definition families: %w", err)
		}
		families = append(families, out.Families...)
		if out.NextToken == nil {
			break
		}
		nextToken = out.NextToken
	}
	return families, nil
}

// DeregisterAllRevisions deregisters all ACTIVE revisions of a task definition family.
func (c *ECSClient) DeregisterAllRevisions(ctx context.Context, family string) error {
	var nextToken *string
	for {
		out, err := c.client.ListTaskDefinitions(ctx, &ecs.ListTaskDefinitionsInput{
			FamilyPrefix: aws.String(family),
			Status:       types.TaskDefinitionStatusActive,
			NextToken:    nextToken,
		})
		if err != nil {
			return fmt.Errorf("listing task definitions for family %q: %w", family, err)
		}
		for _, arn := range out.TaskDefinitionArns {
			if err := c.DeregisterTaskDefinition(ctx, arn); err != nil {
				return err
			}
		}
		if out.NextToken == nil {
			break
		}
		nextToken = out.NextToken
	}
	return nil
}

// RunTaskOptions holds parameters for launching an ECS task.
type RunTaskOptions struct {
	TaskDefinitionARN     string
	Subnets               []string
	SecurityGroups        []string
	EnvVars               map[string]string
	UseSpot               bool
	ContainerEnvOverrides map[string]string // per-task env var overrides applied at RunTask time
}

// TaskStatus represents the status of a running ECS task.
type TaskStatus struct {
	TaskARN    string
	Status     string // PROVISIONING|PENDING|RUNNING|STOPPED
	ExitCode   *int32
	StopReason string
	Failed     bool
}

// RegisterTaskDefinition registers an ECS task definition for a burst worker.
func (c *ECSClient) RegisterTaskDefinition(
	ctx context.Context,
	family string,
	image string,
	cpu, memoryMB int,
	executionRoleARN, taskRoleARN string,
	env map[string]string,
) (string, error) {
	envVars := make([]types.KeyValuePair, 0, len(env))
	for k, v := range env {
		k, v := k, v
		envVars = append(envVars, types.KeyValuePair{
			Name:  aws.String(k),
			Value: aws.String(v),
		})
	}

	out, err := c.client.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String(family),
		NetworkMode:             types.NetworkModeAwsvpc,
		RequiresCompatibilities: []types.Compatibility{types.CompatibilityFargate},
		Cpu:                     aws.String(fmt.Sprintf("%d", cpu*1024)),
		Memory:                  aws.String(fmt.Sprintf("%d", memoryMB)),
		ExecutionRoleArn:        aws.String(executionRoleARN),
		TaskRoleArn:             aws.String(taskRoleARN),
		ContainerDefinitions: []types.ContainerDefinition{
			{
				Name:        aws.String("worker"),
				Image:       aws.String(image),
				Essential:   aws.Bool(true),
				Environment: envVars,
			},
		},
		Tags: []types.Tag{
			{Key: aws.String("managed-by"), Value: aws.String("burst-core")},
		},
	})
	if err != nil {
		return "", fmt.Errorf("registering task definition %q: %w", family, err)
	}

	return aws.ToString(out.TaskDefinition.TaskDefinitionArn), nil
}

// RunTask launches a single ECS Fargate task and returns its ARN.
func (c *ECSClient) RunTask(ctx context.Context, opts RunTaskOptions) (string, error) {
	launchType := types.LaunchTypeFargate
	capacityStrategy := []types.CapacityProviderStrategyItem{}

	if opts.UseSpot {
		capacityStrategy = []types.CapacityProviderStrategyItem{
			{CapacityProvider: aws.String("FARGATE_SPOT"), Weight: 1},
		}
		launchType = "" // cannot set both launchType and capacityProviderStrategy
	}

	in := &ecs.RunTaskInput{
		Cluster:        aws.String(c.cluster),
		TaskDefinition: aws.String(opts.TaskDefinitionARN),
		NetworkConfiguration: &types.NetworkConfiguration{
			AwsvpcConfiguration: &types.AwsVpcConfiguration{
				Subnets:        opts.Subnets,
				SecurityGroups: opts.SecurityGroups,
				AssignPublicIp: types.AssignPublicIpEnabled,
			},
		},
		Tags: []types.Tag{
			{Key: aws.String("managed-by"), Value: aws.String("burst-core")},
		},
	}

	if opts.UseSpot {
		in.CapacityProviderStrategy = capacityStrategy
	} else {
		in.LaunchType = launchType
	}

	if len(opts.ContainerEnvOverrides) > 0 {
		overrideEnvVars := make([]types.KeyValuePair, 0, len(opts.ContainerEnvOverrides))
		for k, v := range opts.ContainerEnvOverrides {
			k, v := k, v
			overrideEnvVars = append(overrideEnvVars, types.KeyValuePair{
				Name:  aws.String(k),
				Value: aws.String(v),
			})
		}
		in.Overrides = &types.TaskOverride{
			ContainerOverrides: []types.ContainerOverride{{
				Name:        aws.String("worker"),
				Environment: overrideEnvVars,
			}},
		}
	}

	out, err := c.client.RunTask(ctx, in)
	if err != nil {
		return "", fmt.Errorf("running task: %w", err)
	}
	if len(out.Failures) > 0 {
		f := out.Failures[0]
		return "", fmt.Errorf("ECS task failure: %s — %s",
			aws.ToString(f.Reason), aws.ToString(f.Detail))
	}
	if len(out.Tasks) == 0 {
		return "", fmt.Errorf("RunTask returned no tasks and no failures")
	}

	return aws.ToString(out.Tasks[0].TaskArn), nil
}

// StopTask stops a running ECS task.
func (c *ECSClient) StopTask(ctx context.Context, taskARN, reason string) error {
	_, err := c.client.StopTask(ctx, &ecs.StopTaskInput{
		Cluster: aws.String(c.cluster),
		Task:    aws.String(taskARN),
		Reason:  aws.String(reason),
	})
	return err
}

// DescribeTasks returns the current status of the given task ARNs.
// At most 100 ARNs can be queried per call (AWS limit).
func (c *ECSClient) DescribeTasks(ctx context.Context, taskARNs []string) ([]TaskStatus, error) {
	out, err := c.client.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(c.cluster),
		Tasks:   taskARNs,
	})
	if err != nil {
		return nil, fmt.Errorf("describing tasks: %w", err)
	}

	statuses := make([]TaskStatus, 0, len(out.Tasks))
	for _, t := range out.Tasks {
		ts := TaskStatus{
			TaskARN:    aws.ToString(t.TaskArn),
			Status:     aws.ToString(t.LastStatus),
			StopReason: aws.ToString(t.StoppedReason),
		}

		if ts.Status == "STOPPED" {
			for _, c := range t.Containers {
				if c.ExitCode != nil {
					ts.ExitCode = c.ExitCode
					ts.Failed = *c.ExitCode != 0
				}
			}
		}

		statuses = append(statuses, ts)
	}
	return statuses, nil
}

// DeregisterTaskDefinition deregisters an ECS task definition.
func (c *ECSClient) DeregisterTaskDefinition(ctx context.Context, arn string) error {
	_, err := c.client.DeregisterTaskDefinition(ctx, &ecs.DeregisterTaskDefinitionInput{
		TaskDefinition: aws.String(arn),
	})
	if err != nil {
		var notFound *types.ResourceNotFoundException
		if errors.As(err, &notFound) {
			return nil
		}
		return fmt.Errorf("deregistering task definition %q: %w", arn, err)
	}
	return nil
}
