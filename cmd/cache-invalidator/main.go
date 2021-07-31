package main

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/go-redis/redis/v8"
	log "github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

var ctx = context.Background()

func aquire_lock(rdb *redis.Client, deploymentName string) {
	log.Debug("Acquiring lock for " + deploymentName)
	err := rdb.Set(ctx, "lock:"+deploymentName, 1, 30*time.Minute).Err()
	if err != nil {
		panic(err)
	}
}

func release_lock(rdb *redis.Client, deploymentName string) {
	log.Debug("Releasing lock for " + deploymentName)
	err := rdb.Del(ctx, "lock:"+deploymentName).Err()
	if err != nil {
		panic(err)
	}
}

// Identify whether the currentTag is new, If it is a deployment is in progress or already happened
func isNewTag(deploymentName string, currentTag string, rdb *redis.Client) bool {
	// Check current tag for this deployment
	latestTag, err := rdb.Get(ctx, deploymentName).Result()
	if err == redis.Nil {
		log.Info(deploymentName + " does not exist ... adding to rdb with tag " + currentTag)
		//Missing tag adding current and skipping validation
		err := rdb.Set(ctx, deploymentName, currentTag, 0).Err()
		if err != nil {
			panic(err)
		}
		return false
	}
	if err != nil {
		panic(err)
	}

	// New tag detected, validate deployment complete, if not continue ...
	if latestTag != currentTag {
		//Try to aquire lock on this deployment - making sure no other goroutine is already handling this new tag request
		_, err = rdb.Get(ctx, "lock:"+deploymentName).Result()
		if err == redis.Nil {
			aquire_lock(rdb, deploymentName)
		} else if err != nil {
			panic(err)
		} else {
			// lock already aquired exit function
			log.Debug("Lock already aquired by " + deploymentName)
			return false
		}

		// New tag, Update Redis and invalidate cache
		log.Info("New tag detected for " + deploymentName + " - " + currentTag + ", waiting for upgrade to complete ...")
		return true
	}
	return false
}

func main() {

	loglevel := os.Getenv("LOGLEVEL")
	if loglevel == "DEBUG" {
		log.SetLevel(log.DebugLevel)
	} else {
		log.SetLevel(log.InfoLevel)
	}

	// The namespace to iterate in
	namespace := os.Getenv("NAMESPACE")
	if len(namespace) == 0 {
		panic("Excpecting namespace env var")
	}
	// Redis connection : host
	host := os.Getenv("REDIS_HOST")
	if len(host) == 0 {
		panic("Excpecting redis host env var")
	}
	// Redis connection : port
	port := os.Getenv("REDIS_PORT")
	if len(port) == 0 {
		panic("Excpecting redis port env var")
	}

	/*
	 Remote redis hosts in other cluster groups (port included), e.g. host1:port1,host2:port2...
	 cache-invalidator is working in a cluster like env where it needs to confirm succesful rollout not only with the inner cluster workloads but also
	 workloads from external cluster groups - that is achieved via Redis
	*/
	remoteRedisHosts := os.Getenv("REMOTE_REDIS_HOSTS")

	// KubeConfig path
	// KubeConfigPath := os.Getenv("KUBE_CONFIG_PATH")

	//AWS connection session
	var sess *session.Session
	sess, err := session.NewSession()
	if err != nil {
		panic(err)
	}

	// creates the in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		panic(err.Error())
	}

	// Create external cluster config
	// var kubeconfig *string
	// kubeconfig = flag.String("kubeconfig", KubeConfigPath, "(optional) absolute path to the kubeconfig file")
	// flag.Parse()
	// config, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	// if err != nil {
	// 	panic(err)
	// }

	// creates the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	// Local redis client
	rdb := redis.NewClient(&redis.Options{
		Addr:     host + ":" + port,
		Password: "", // no password set
		DB:       0,  // use default DB
	})

	var remoterdbs []*redis.Client
	// Remote redis clients if provided (i.e. cluster groups arch)
	if len(remoteRedisHosts) != 0 {
		remoteRedisHostsSliced := strings.Split(remoteRedisHosts, ",")
		for _, remoteHost := range remoteRedisHostsSliced {
			host := strings.Split(remoteHost, "__")[0]
			port := strings.Split(remoteHost, "__")[1]
			remoterdbs = append(remoterdbs,
				redis.NewClient(&redis.Options{
					Addr:     host + ":" + port,
					Password: "", // no password set
					DB:       0,  // use default DB
				}))
		}
	}

	for {
		time.Sleep(10 * time.Second)
		deploymentsClient := clientset.AppsV1().Deployments(namespace)
		ingressesClient := clientset.ExtensionsV1beta1().Ingresses(namespace)
		list, err := deploymentsClient.List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			log.Error("Error while getting deployments list in " + namespace + "namespace")
			panic(err)
		}
		// For every workload in a given namespace
		for _, d := range list.Items {
			image := d.Spec.Template.Spec.Containers[0].Image
			tag := strings.Split(image, ":")[1]
			log.Debug("* " + d.Name + ", tag = " + tag)
			// New tag means new deployment, i.e. invalidation is needed
			if isNewTag(d.Name, tag, rdb) {
				// Get the ingress object to retrieve the host name (needed to get the correct CF distribution)
				ingress, err := ingressesClient.Get(context.TODO(), d.Name, metav1.GetOptions{})
				if err != nil {
					log.Error("Error while getting ingress object from " + d.Name + ", Check if object exists")
					release_lock(rdb, d.Name)
					continue
				}
				host := ingress.Spec.Rules[0].Host
				go invalidate(sess, getDistributionID(sess, host), clientset, namespace, d.Name, rdb, tag, remoterdbs)
			}
		}
	}
}
