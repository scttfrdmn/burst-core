package aws

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/scttfrdmn/burst-core/pkg/protocol"
)

type mockECS struct {
	describeClusters          func(context.Context, *ecs.DescribeClustersInput, ...func(*ecs.Options)) (*ecs.DescribeClustersOutput, error)
	createCluster             func(context.Context, *ecs.CreateClusterInput, ...func(*ecs.Options)) (*ecs.CreateClusterOutput, error)
	deleteCluster             func(context.Context, *ecs.DeleteClusterInput, ...func(*ecs.Options)) (*ecs.DeleteClusterOutput, error)
	registerTaskDefinition    func(context.Context, *ecs.RegisterTaskDefinitionInput, ...func(*ecs.Options)) (*ecs.RegisterTaskDefinitionOutput, error)
	deregisterTaskDefinition  func(context.Context, *ecs.DeregisterTaskDefinitionInput, ...func(*ecs.Options)) (*ecs.DeregisterTaskDefinitionOutput, error)
	runTask                   func(context.Context, *ecs.RunTaskInput, ...func(*ecs.Options)) (*ecs.RunTaskOutput, error)
	stopTask                  func(context.Context, *ecs.StopTaskInput, ...func(*ecs.Options)) (*ecs.StopTaskOutput, error)
	describeTasks             func(context.Context, *ecs.DescribeTasksInput, ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error)
}

func (m *mockECS) DescribeClusters(ctx context.Context, in *ecs.DescribeClustersInput, opts ...func(*ecs.Options)) (*ecs.DescribeClustersOutput, error) {
	return m.describeClusters(ctx, in, opts...)
}
func (m *mockECS) CreateCluster(ctx context.Context, in *ecs.CreateClusterInput, opts ...func(*ecs.Options)) (*ecs.CreateClusterOutput, error) {
	return m.createCluster(ctx, in, opts...)
}
func (m *mockECS) DeleteCluster(ctx context.Context, in *ecs.DeleteClusterInput, opts ...func(*ecs.Options)) (*ecs.DeleteClusterOutput, error) {
	if m.deleteCluster != nil {
		return m.deleteCluster(ctx, in, opts...)
	}
	return &ecs.DeleteClusterOutput{}, nil
}
func (m *mockECS) RegisterTaskDefinition(ctx context.Context, in *ecs.RegisterTaskDefinitionInput, opts ...func(*ecs.Options)) (*ecs.RegisterTaskDefinitionOutput, error) {
	return m.registerTaskDefinition(ctx, in, opts...)
}
func (m *mockECS) DeregisterTaskDefinition(ctx context.Context, in *ecs.DeregisterTaskDefinitionInput, opts ...func(*ecs.Options)) (*ecs.DeregisterTaskDefinitionOutput, error) {
	if m.deregisterTaskDefinition != nil {
		return m.deregisterTaskDefinition(ctx, in, opts...)
	}
	return &ecs.DeregisterTaskDefinitionOutput{TaskDefinition: &types.TaskDefinition{}}, nil
}
func (m *mockECS) RunTask(ctx context.Context, in *ecs.RunTaskInput, opts ...func(*ecs.Options)) (*ecs.RunTaskOutput, error) {
	return m.runTask(ctx, in, opts...)
}
func (m *mockECS) StopTask(ctx context.Context, in *ecs.StopTaskInput, opts ...func(*ecs.Options)) (*ecs.StopTaskOutput, error) {
	if m.stopTask != nil {
		return m.stopTask(ctx, in, opts...)
	}
	return &ecs.StopTaskOutput{}, nil
}
func (m *mockECS) DescribeTasks(ctx context.Context, in *ecs.DescribeTasksInput, opts ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error) {
	return m.describeTasks(ctx, in, opts...)
}

func TestCreateCluster_alreadyActive(t *testing.T) {
	c := &ECSClient{client: &mockECS{
		describeClusters: func(_ context.Context, _ *ecs.DescribeClustersInput, _ ...func(*ecs.Options)) (*ecs.DescribeClustersOutput, error) {
			return &ecs.DescribeClustersOutput{
				Clusters: []types.Cluster{
					{ClusterName: aws.String("burst-cluster"), Status: aws.String("ACTIVE")},
				},
			}, nil
		},
		createCluster: func(_ context.Context, _ *ecs.CreateClusterInput, _ ...func(*ecs.Options)) (*ecs.CreateClusterOutput, error) {
			t.Fatal("CreateCluster should not be called if already active")
			return nil, nil
		},
	}, cluster: "burst-cluster"}

	if err := c.CreateCluster(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCreateCluster_new(t *testing.T) {
	created := false
	c := &ECSClient{client: &mockECS{
		describeClusters: func(_ context.Context, _ *ecs.DescribeClustersInput, _ ...func(*ecs.Options)) (*ecs.DescribeClustersOutput, error) {
			return &ecs.DescribeClustersOutput{Clusters: []types.Cluster{}}, nil
		},
		createCluster: func(_ context.Context, _ *ecs.CreateClusterInput, _ ...func(*ecs.Options)) (*ecs.CreateClusterOutput, error) {
			created = true
			return &ecs.CreateClusterOutput{
				Cluster: &types.Cluster{ClusterName: aws.String("burst-cluster"), Status: aws.String("ACTIVE")},
			}, nil
		},
	}, cluster: "burst-cluster"}

	if err := c.CreateCluster(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !created {
		t.Error("expected CreateCluster to be called")
	}
}

func TestCreateCluster_error(t *testing.T) {
	c := &ECSClient{client: &mockECS{
		describeClusters: func(_ context.Context, _ *ecs.DescribeClustersInput, _ ...func(*ecs.Options)) (*ecs.DescribeClustersOutput, error) {
			return nil, errors.New("AccessDenied")
		},
	}, cluster: "burst-cluster"}

	err := c.CreateCluster(context.Background())
	var setupErr *protocol.BurstSetupError
	if !errors.As(err, &setupErr) {
		t.Errorf("expected BurstSetupError, got %T: %v", err, err)
	}
}

func TestRunTask_success(t *testing.T) {
	const taskARN = "arn:aws:ecs:us-east-1:123:task/burst-cluster/abc123"
	c := &ECSClient{client: &mockECS{
		runTask: func(_ context.Context, _ *ecs.RunTaskInput, _ ...func(*ecs.Options)) (*ecs.RunTaskOutput, error) {
			return &ecs.RunTaskOutput{
				Tasks: []types.Task{{TaskArn: aws.String(taskARN)}},
			}, nil
		},
	}, cluster: "burst-cluster"}

	arn, err := c.RunTask(context.Background(), RunTaskOptions{
		TaskDefinitionARN: "arn:aws:ecs:us-east-1:123:task-definition/burst-session:1",
		Subnets:           []string{"subnet-aaa"},
		SecurityGroups:    []string{"sg-default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if arn != taskARN {
		t.Errorf("got %q, want %q", arn, taskARN)
	}
}

func TestDescribeTasks(t *testing.T) {
	const taskARN = "arn:aws:ecs:us-east-1:123:task/burst-cluster/abc123"
	exitCode := int32(0)
	c := &ECSClient{client: &mockECS{
		describeTasks: func(_ context.Context, _ *ecs.DescribeTasksInput, _ ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error) {
			return &ecs.DescribeTasksOutput{
				Tasks: []types.Task{
					{
						TaskArn:    aws.String(taskARN),
						LastStatus: aws.String("STOPPED"),
						Containers: []types.Container{
							{ExitCode: &exitCode},
						},
					},
				},
			}, nil
		},
	}, cluster: "burst-cluster"}

	statuses, err := c.DescribeTasks(context.Background(), []string{taskARN})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(statuses) != 1 {
		t.Fatalf("got %d statuses, want 1", len(statuses))
	}
	if statuses[0].Status != "STOPPED" {
		t.Errorf("status: got %q, want \"STOPPED\"", statuses[0].Status)
	}
	if statuses[0].Failed {
		t.Error("exit code 0 should not be failed")
	}
}
