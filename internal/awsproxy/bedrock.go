package awsproxy

import (
	"context"
	"fmt"

	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
)

// BedrockClient wraps the AWS Bedrock Runtime client.
type BedrockClient struct {
	client *bedrockruntime.Client
	region string
}

// NewBedrockClient creates a BedrockClient using static credentials.
func NewBedrockClient(region, accessKeyID, secretAccessKey string) (*BedrockClient, error) {
	cfg, err := awscfg.LoadDefaultConfig(context.Background(),
		awscfg.WithRegion(region),
		awscfg.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, ""),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	return &BedrockClient{
		client: bedrockruntime.NewFromConfig(cfg),
		region: region,
	}, nil
}

// InvokeModel sends a synchronous request to Bedrock and returns the response body.
func (bc *BedrockClient) InvokeModel(ctx context.Context, modelID string, body []byte) ([]byte, error) {
	out, err := bc.client.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
		ModelId:     &modelID,
		Body:        body,
		ContentType: strPtr("application/json"),
		Accept:      strPtr("application/json"),
	})
	if err != nil {
		return nil, fmt.Errorf("bedrock invoke: %w", err)
	}
	return out.Body, nil
}

// InvokeModelStream sends a streaming request and returns the raw output for event iteration.
func (bc *BedrockClient) InvokeModelStream(ctx context.Context, modelID string, body []byte) (*bedrockruntime.InvokeModelWithResponseStreamOutput, error) {
	out, err := bc.client.InvokeModelWithResponseStream(ctx, &bedrockruntime.InvokeModelWithResponseStreamInput{
		ModelId:     &modelID,
		Body:        body,
		ContentType: strPtr("application/json"),
		Accept:      strPtr("application/json"),
	})
	if err != nil {
		return nil, fmt.Errorf("bedrock stream invoke: %w", err)
	}
	return out, nil
}

func strPtr(s string) *string { return &s }
