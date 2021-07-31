## cache invalidator

Invalidate cache (Cloud front) when stage or prod upgrade occurs

## Info

This worker will continuously go over all the deployments in a give namespace and will cache all the current tags.
If a tag changes, i.e. upgrade occured, the worker will get the site domain from the ingress object,find the corresponding CF distribution and will invalidate it.
The invalidation will only happen once the new deployment rolled out succesfully otherwise the worker will wait for all the pods to be in a running state. 
Waiting for pods is a non blocking operation as goroutines are used, i.e. each invalidation is happening concurrently.
If a deployment is unsuccessful (and is rolled back) the waiting for pods procedure will timeout and skip invalidation

## Cluster groups

A highly available (K8S) architecture with multiple clusters hosting the same workloads with a single routing point are called cluster groups. In this arch the worker needs :
1. For a signle rollout across all the cluster groups only 1 invalidaion should happens
2. Invalidation happens only when all the workloads (across all the cluster groups) were rolled out successfuly 

This worker uses redis instances to implement this logic

## Env vars
* `REDIS_HOST` - Redis domain to cache all the current tags
* `REDIS_PORT` - Redis port
* `REMOTE_REDIS_HOSTS` - Comma seperated list of remote redis hosts (redis for every cluster in the cluster group), If empty the worker is working in a non cluster groups mode
* `LOGLEVEL` - Worker log level
* `AWS_ACCESS_KEY_ID` - aws key with CF invalidation privileges
* `AWS_SECRET_ACCESS_KEY` - aws secret key
* `WEBHOOKS_CONFIG` - path to webhook config file

## Webhooks
This controller also supports post invalidation actions via webhooks.
If `WEBHOOKS_CONFIG` is set - pointing to the webhook config file (json), the controller will trigger a webhook once invalidation is complete. 
In case the invalidation takes too long to complete, a timeout will be issued for this "invalidation" process. An example of the config file can be found under configs/webhooks.go

# How to install 
TODO