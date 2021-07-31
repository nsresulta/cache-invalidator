package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	deployRunningThreshold     = time.Second * 120
	deployRunningCheckInterval = time.Second * 10
	podSelector                = "workload.user.cattle.io/workloadselector"
)

func podContainersRunning(clientSet *kubernetes.Clientset, deploymentName string, namespace string, tag string) (bool, error) {
	//Validate that the actual tag on the deployment didn't change. If it did this goroutine is now checking running pods for an irelevant tag and needs to exit
	deploymentsClient := clientSet.AppsV1().Deployments(namespace)
	deployment, err := deploymentsClient.Get(context.TODO(), deploymentName, metav1.GetOptions{})
	if err != nil {
		return false, err
	}
	image := deployment.Spec.Template.Spec.Containers[0].Image
	currentTag := strings.Split(image, ":")[1]
	// Do we still have the original new tag this goroutine started working on - or do we have a new one (workload rolledback/new upgrade etc.)
	if currentTag != tag {
		return false, fmt.Errorf("Deployment " + deploymentName + " updated while waiting on pods to complete for tag: " + tag + ", Skipping invalidation")
	}

	pods, err := clientSet.CoreV1().Pods(namespace).List(context.TODO(), metav1.ListOptions{
		LabelSelector: fmt.Sprintf(podSelector+"=%s", "deployment-"+namespace+"-"+deploymentName),
	})
	if err != nil {
		return false, err
	}
	if len(pods.Items) == 0 {
		return false, fmt.Errorf("No pods found for selector label : deployment-" + namespace + "-" + deploymentName + ", Skipping invalidation")
	}

	for _, item := range pods.Items {
		for _, condition := range item.Status.Conditions {
			if condition.Type == "Ready" && condition.Status == "False" {
				return false, nil
			}
		}
	}
	return true, nil
}

func waitForPodContainersRunning(clientSet *kubernetes.Clientset, deploymentName string, namespace string, tag string) error {
	end := time.Now().Add(deployRunningThreshold)

	for true {
		<-time.NewTimer(deployRunningCheckInterval).C

		var err error
		running, err := podContainersRunning(clientSet, deploymentName, namespace, tag)
		if running {
			return nil
		}

		if err != nil {
			return err
		}

		if time.Now().After(end) {
			return fmt.Errorf("Some of " + deploymentName + " Pods are not starting ... skipping invalidation")
		}
	}
	return nil
}
