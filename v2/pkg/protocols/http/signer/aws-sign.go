package signer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
)

// AWSOptions
type AWSOptions struct {
	AwsID          string
	AwsSecretToken string
	Service        string
	Region         string
}

// Validate Signature Arguments
func (a *AWSOptions) Validate() error {
	if a.Service == "" {
		return errors.New("aws service cannot be empty")
	}
	if a.Region == "" {
		return errors.New("aws region cannot be empty")
	}

	return nil
}

// AWS v4 signer
type AWSSigner struct {
	creds   *aws.Credentials
	signer  *v4.Signer
	options *AWSOptions
}

// SignHTTP
func (a *AWSSigner) SignHTTP(ctx context.Context, request *http.Request) error {
	if region, ok := ctx.Value(SignerArg("region")).(string); ok && region != "" {
		a.options.Region = region
	}
	if service, ok := ctx.Value(SignerArg("service")).(string); ok && service != "" {
		a.options.Service = service
	}
	if err := a.options.Validate(); err != nil {
		return err
	}

	return a.signer.SignHTTP(ctx, *a.creds, request, a.getPayloadHash(request), a.options.Service, a.options.Region, time.Now())
}

// getPayloadHash returns hex encoded SHA-256 of request body
func (a *AWSSigner) getPayloadHash(request *http.Request) string {
	if request.Body == nil {
		// Default Hash of Empty Payload
		return "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	}

	// no need to close request body since it is a reusablereadercloser
	bin, _ := io.ReadAll(request.Body)
	sha256Hash := sha256.Sum256(bin)
	return hex.EncodeToString(sha256Hash[:])
}

// NewAwsSigner
func NewAwsSigner(opts *AWSOptions) (*AWSSigner, error) {
	credcache := aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(opts.AwsID, opts.AwsSecretToken, ""))
	awscred, err := credcache.Retrieve(context.TODO())
	if err != nil {
		return nil, err
	}
	return &AWSSigner{
		creds:   &awscred,
		options: opts,
		signer:  v4.NewSigner(),
	}, nil
}

// NewAwsSignerFromConfig
func NewAwsSignerFromConfig(opts *AWSOptions) (*AWSSigner, error) {
	/*
		NewAwsSignerFromConfig fetches credentials from both
		1. Environment Variables (old & new)
		2. Shared Credentials ($HOME/.aws)
	*/
	cfg, err := awsconfig.LoadDefaultConfig(context.TODO())
	if err != nil {
		return nil, err
	}
	credcache := aws.NewCredentialsCache(cfg.Credentials)
	awscred, err := credcache.Retrieve(context.TODO())
	if err != nil {
		return nil, err
	}
	return &AWSSigner{
		creds:   &awscred,
		options: opts,
		signer: v4.NewSigner(func(signer *v4.SignerOptions) {
			// signer.DisableURIPathEscaping = true
		}),
	}, nil
}

var AwsSkipList = map[string]interface{}{
	"region": struct{}{},
}

var AwsInternalOnlyVars = map[string]interface{}{
	"aws-id":     struct{}{},
	"aws-secret": struct{}{},
}
