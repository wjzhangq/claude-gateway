package awsproxy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/wjzhangq/claude-gateway/internal/logger"
	"golang.org/x/net/proxy"
)

// BedrockClient wraps the AWS Bedrock Runtime client.
type BedrockClient struct {
	client *bedrockruntime.Client
	region string
}

// NewBedrockClient creates a BedrockClient using static credentials.
// If socks5Proxy is non-empty (e.g. "socks5://127.0.0.1:1080"), all requests
// to Bedrock will be routed through that SOCKS5 proxy.
func NewBedrockClient(region, accessKeyID, secretAccessKey, socks5Proxy string) (*BedrockClient, error) {
	opts := []func(*awscfg.LoadOptions) error{
		awscfg.WithRegion(region),
		awscfg.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, ""),
		),
	}

	if socks5Proxy != "" {
		transport, err := buildSocks5Transport(socks5Proxy)
		if err != nil {
			return nil, fmt.Errorf("build socks5 transport: %w", err)
		}
		httpClient := &http.Client{Transport: transport}
		opts = append(opts, awscfg.WithHTTPClient(httpClient))
		logger.Infof("bedrock: SOCKS5 proxy enabled, addr=%s", socks5Proxy)
	} else {
		logger.Infof("bedrock: using direct connection (no proxy)")
	}

	cfg, err := awscfg.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	return &BedrockClient{
		client: bedrockruntime.NewFromConfig(cfg),
		region: region,
	}, nil
}

// buildSocks5Transport creates an http.RoundTripper that dials through a SOCKS5 proxy.
// Supported formats:
//   - socks5://user:pass@host:port
//   - socks5://host:port
//   - user:pass@host:port  (scheme defaults to socks5://)
//   - host:port
func buildSocks5Transport(proxyAddr string) (http.RoundTripper, error) {
	// Normalize: add scheme if missing so url.Parse works correctly
	raw := proxyAddr
	if !strings.HasPrefix(raw, "socks5://") && !strings.HasPrefix(raw, "socks5h://") {
		raw = "socks5://" + raw
	}

	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse socks5 proxy URL %q: %w", proxyAddr, err)
	}

	addr := u.Host // host:port

	var auth *proxy.Auth
	if u.User != nil {
		auth = &proxy.Auth{User: u.User.Username()}
		auth.Password, _ = u.User.Password()
	}

	logger.Infof("bedrock: creating socks5 dialer, addr=%s hasAuth=%v", addr, auth != nil)

	dialer, err := proxy.SOCKS5("tcp", addr, auth, proxy.Direct)
	if err != nil {
		return nil, fmt.Errorf("create socks5 dialer for %q: %w", addr, err)
	}

	contextDialer, ok := dialer.(proxy.ContextDialer)
	if ok {
		logger.Debugf("bedrock: socks5 transport built with ContextDialer, proxy=%s", addr)
		return &http.Transport{
			DialContext: contextDialer.DialContext,
		}, nil
	}

	// Fallback: wrap plain Dialer
	logger.Debugf("bedrock: socks5 transport built with fallback Dialer, proxy=%s", addr)
	return &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return dialer.Dial(network, address)
		},
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
