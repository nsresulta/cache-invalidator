package main

import (
	"encoding/json"
	"io/ioutil"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"

	log "github.com/sirupsen/logrus"
)

var webhooks map[string]Webhook

func init() {
	config, set := os.LookupEnv("WEBHOOKS_CONFIG")
	if !set || config == "" {
		return
	}
	jsonFile, err := os.Open(config)
	if err != nil {
		panic(err)
	}

	byteValue, _ := ioutil.ReadAll(jsonFile)
	var webhooklist Webhooks
	webhooks = make(map[string]Webhook)
	json.Unmarshal(byteValue, &webhooklist)

	// Create webooks map
	for i := 0; i < len(webhooklist.Webhooks); i++ {
		webhooks[webhooklist.Webhooks[i].Name] = webhooklist.Webhooks[i]
	}
	defer jsonFile.Close()
}

type Webhooks struct {
	Webhooks []Webhook `json:"webhooks"`
}

type Webhook struct {
	Name       string            `json:"name"`
	Url        string            `json:"url"`
	Method     string            `json:"method"`
	Headers    map[string]string `json:"headers"`
	Parameters map[string]string `json:"parameters"`
}

func getWebHookByName(name string) Webhook {
	return webhooks[name]
}

func execWebhook(name string) {

	_, found := webhooks[name]
	if !found {
		log.Info("Webhook for " + name + " not found")
		return
	}

	webhookurl := webhooks[name].Url
	if webhookurl == "" {
		log.Error("Missing webhook url - check config")
		return
	}
	method := webhooks[name].Method
	if method == "" {
		log.Error("Missing webhook method - check config")
		return
	}

	patameters := webhooks[name].Parameters
	headers := webhooks[name].Headers
	log.Debug("Creating webhook request ...")
	httpClient := &http.Client{}
	urlParameters := url.Values{}
	for key, value := range patameters {
		urlParameters.Add(key, value)
	}
	req, err := http.NewRequest(method, webhookurl, strings.NewReader(urlParameters.Encode()))
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	log.Info("Calling webhook " + webhookurl)

	dump, err := httputil.DumpRequestOut(req, true)
	log.Debug(string(dump))
	if err != nil {
		log.Fatal(err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		log.Error(err)
	}

	defer resp.Body.Close()

	log.Info("Status code for " + webhookurl + " " + strconv.Itoa(resp.StatusCode))

}
