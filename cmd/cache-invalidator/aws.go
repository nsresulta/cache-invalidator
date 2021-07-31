package main

import (
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/cloudfront"
	"github.com/go-redis/redis/v8"
	log "github.com/sirupsen/logrus"
	"k8s.io/client-go/kubernetes"
)

const (
	InvalidationThreshold     = time.Second * 600
	InvalidationCheckInterval = time.Second * 30
)

func waitForClusterGroups(deploymentName string, tag string, remoteRedisHost *redis.Client) error {
	end := time.Now().Add(time.Second * 120)

	for true {
		<-time.NewTimer(time.Second * 10).C

		log.Debug("Waiting for " + deploymentName + ":" + tag + " rollout on remote cluster - " + remoteRedisHost.String())
		latestTag, err := remoteRedisHost.Get(ctx, deploymentName).Result()
		if err != nil {
			return fmt.Errorf("Error while trying to query for " + deploymentName + ":" + tag + " in other clusters - " + remoteRedisHost.String() + ":" + err.Error())
		}
		if latestTag == tag {
			return nil
		}
		if time.Now().After(end) {
			return fmt.Errorf("Rollout of " + deploymentName + ":" + tag + " did not complete in time in other cluster groups")
		}
	}
	return nil
}

// Get the distribution ID based on the domain
func getDistributionID(sess *session.Session, domain string) string {
	svc := cloudfront.New(sess)
	ID := ""
	for true {
		var result *cloudfront.ListDistributionsOutput
		// Get the CF dist list in chunks of 50
		input := &cloudfront.ListDistributionsInput{
			Marker:   aws.String(ID),
			MaxItems: aws.Int64(50),
		}
		result, err := svc.ListDistributions(input)
		if err != nil {
			if aerr, ok := err.(awserr.Error); ok {
				switch aerr.Code() {
				case cloudfront.ErrCodeInvalidArgument:
					log.Error(cloudfront.ErrCodeInvalidArgument, aerr.Error())
				default:
					log.Error(aerr.Error())
				}
			} else {
				// Print the error, cast err to awserr.Error to get the Code and
				// Message from an error.
				log.Error(err.Error())
			}
			return ""
		}

		// If no more distribution chunks left exit the loop
		if result.DistributionList.Items == nil {
			break
		}

		// For every distribution
		for _, d := range result.DistributionList.Items {
			// For every CNAME in a distribution
			for _, c := range d.Aliases.Items {
				if *c == domain {
					return *d.Id
				}
			}
			// Paginating results, i.e. using ID as the marker to indicate where to begin in the list for the next call
			ID = *d.Id
		}
	}

	log.Info("Couldn't find distribution id for " + domain)
	return ""
}

// Invalidate cache
func invalidate(sess *session.Session, distID string, clientset *kubernetes.Clientset, namespace string, deploymentName string, rdb *redis.Client, tag string, remoteRedisHosts []*redis.Client) {
	if distID == "" {
		release_lock(rdb, deploymentName)
		return
	}

	// Confirm rolling upgrade complete before invalidating (5 min timeout)
	err := waitForPodContainersRunning(clientset, deploymentName, namespace, tag)
	if err != nil {
		log.Error(err)
		release_lock(rdb, deploymentName)
		return
	}

	log.Debug("Upgrade of " + deploymentName + " completed with success, updating new tag " + tag)
	err = rdb.Set(ctx, deploymentName, tag, 0).Err()
	if err != nil {
		panic(err)
	}
	release_lock(rdb, deploymentName)

	// Confirm upgrade completed on all other clusters (deploymentName-tag is a distributed workload across multiple clusters with only 1 CDN instance that needs invalidation)
	log.Debug("Waiting for successful rollout on other clusters if available")
	for _, remoteRedisHost := range remoteRedisHosts {
		err := waitForClusterGroups(deploymentName, tag, remoteRedisHost)
		if err != nil {
			log.Error(err)
			return
		}
	}

	log.Info("Invalidating cache for distribution " + distID)
	svc := cloudfront.New(sess)
	input := &cloudfront.CreateInvalidationInput{
		DistributionId: aws.String(distID),
		InvalidationBatch: &cloudfront.InvalidationBatch{
			CallerReference: aws.String(
				fmt.Sprintf("invalidation-id-%s-%s", deploymentName, tag)),
			Paths: &cloudfront.Paths{
				Quantity: aws.Int64(1),
				Items: []*string{
					aws.String("/*"),
				},
			},
		},
	}

	result, err := svc.CreateInvalidation(input)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			case cloudfront.ErrCodeAccessDenied:
				log.Error(cloudfront.ErrCodeAccessDenied, aerr.Error())
			case cloudfront.ErrCodeMissingBody:
				log.Error(cloudfront.ErrCodeMissingBody, aerr.Error())
			case cloudfront.ErrCodeInvalidArgument:
				log.Error(cloudfront.ErrCodeInvalidArgument, aerr.Error())
			case cloudfront.ErrCodeNoSuchDistribution:
				log.Error(cloudfront.ErrCodeNoSuchDistribution, aerr.Error())
			case cloudfront.ErrCodeBatchTooLarge:
				log.Error(cloudfront.ErrCodeBatchTooLarge, aerr.Error())
			case cloudfront.ErrCodeTooManyInvalidationsInProgress:
				log.Error(cloudfront.ErrCodeTooManyInvalidationsInProgress, aerr.Error())
			case cloudfront.ErrCodeInconsistentQuantities:
				log.Error(cloudfront.ErrCodeInconsistentQuantities, aerr.Error())
			default:
				log.Error(aerr.Error())
			}
		} else {
			// Print the error, cast err to awserr.Error to get the Code and
			// Message from an error.
			log.Error(err.Error())
		}
		return
	}

	log.Info(result)

	// Call post invalidation webhook once invalidation finished (timeout in case invalidation does not complete in time)
	end := time.Now().Add(InvalidationThreshold)
	getInvalInput := &cloudfront.GetInvalidationInput{
		DistributionId: aws.String(distID),
		Id:             result.Invalidation.Id,
	}
	invalidationStatus, err := svc.GetInvalidation(getInvalInput)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			case cloudfront.ErrCodeAccessDenied:
				log.Error(cloudfront.ErrCodeAccessDenied, aerr.Error())
			case cloudfront.ErrCodeMissingBody:
				log.Error(cloudfront.ErrCodeMissingBody, aerr.Error())
			case cloudfront.ErrCodeInvalidArgument:
				log.Error(cloudfront.ErrCodeInvalidArgument, aerr.Error())
			case cloudfront.ErrCodeNoSuchDistribution:
				log.Error(cloudfront.ErrCodeNoSuchDistribution, aerr.Error())
			case cloudfront.ErrCodeBatchTooLarge:
				log.Error(cloudfront.ErrCodeBatchTooLarge, aerr.Error())
			case cloudfront.ErrCodeTooManyInvalidationsInProgress:
				log.Error(cloudfront.ErrCodeTooManyInvalidationsInProgress, aerr.Error())
			case cloudfront.ErrCodeInconsistentQuantities:
				log.Error(cloudfront.ErrCodeInconsistentQuantities, aerr.Error())
			default:
				log.Error(aerr.Error())
			}
		} else {
			// Print the error, cast err to awserr.Error to get the Code and
			// Message from an error.
			log.Error(err.Error())
		}
		return
	}

	for *invalidationStatus.Invalidation.Status != "Completed" {
		<-time.NewTimer(InvalidationCheckInterval).C
		invalidationStatus, err = svc.GetInvalidation(getInvalInput)
		if err != nil {
			if aerr, ok := err.(awserr.Error); ok {
				switch aerr.Code() {
				case cloudfront.ErrCodeAccessDenied:
					log.Error(cloudfront.ErrCodeAccessDenied, aerr.Error())
				case cloudfront.ErrCodeMissingBody:
					log.Error(cloudfront.ErrCodeMissingBody, aerr.Error())
				case cloudfront.ErrCodeInvalidArgument:
					log.Error(cloudfront.ErrCodeInvalidArgument, aerr.Error())
				case cloudfront.ErrCodeNoSuchDistribution:
					log.Error(cloudfront.ErrCodeNoSuchDistribution, aerr.Error())
				case cloudfront.ErrCodeBatchTooLarge:
					log.Error(cloudfront.ErrCodeBatchTooLarge, aerr.Error())
				case cloudfront.ErrCodeTooManyInvalidationsInProgress:
					log.Error(cloudfront.ErrCodeTooManyInvalidationsInProgress, aerr.Error())
				case cloudfront.ErrCodeInconsistentQuantities:
					log.Error(cloudfront.ErrCodeInconsistentQuantities, aerr.Error())
				default:
					log.Error(aerr.Error())
				}
			} else {
				// Print the error, cast err to awserr.Error to get the Code and
				// Message from an error.
				log.Error(err.Error())
			}
			return
		}
		if time.Now().After(end) {
			log.Error(distID + "Took to long to invalidate, timeout, skipping webhook call")
			return
		}
	}

	execWebhook(deploymentName)
}
