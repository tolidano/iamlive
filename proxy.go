package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"embed"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/elazarl/goproxy"
	"github.com/mitchellh/go-homedir"
)

//go:embed service/*
var serviceFiles embed.FS

var serviceDefinitions []ServiceDefinition

func loadCAKeys() error {
	var caCert []byte
	var caKey []byte

	caBundlePath, err := homedir.Expand(*caBundleFlag)
	if err != nil {
		return err
	}
	caKeyPath, err := homedir.Expand(*caKeyFlag)
	if err != nil {
		return err
	}

	if _, err := os.Stat(caBundlePath); os.IsNotExist(err) {
		if _, err := os.Stat(caKeyPath); os.IsNotExist(err) {
			// make directories
			err = os.MkdirAll(filepath.Dir(caBundlePath), 0700)
			if err != nil {
				return err
			}
			err = os.MkdirAll(filepath.Dir(caKeyPath), 0700)
			if err != nil {
				return err
			}

			// generate keys
			ca := &x509.Certificate{
				SerialNumber: big.NewInt(2019),
				Subject: pkix.Name{
					Organization:  []string{"iamlive CA"},
					Country:       []string{"US"},
					Province:      []string{""},
					Locality:      []string{"San Francisco"},
					StreetAddress: []string{"Golden Gate Bridge"},
					PostalCode:    []string{"94016"},
				},
				NotBefore:             time.Now(),
				NotAfter:              time.Now().AddDate(10, 0, 0),
				IsCA:                  true,
				ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
				KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
				BasicConstraintsValid: true,
			}

			caPrivKey, err := rsa.GenerateKey(rand.Reader, 4096)
			if err != nil {
				return err
			}

			caBytes, err := x509.CreateCertificate(rand.Reader, ca, ca, &caPrivKey.PublicKey, caPrivKey)
			if err != nil {
				return err
			}

			caPEM := new(bytes.Buffer)
			pem.Encode(caPEM, &pem.Block{
				Type:  "CERTIFICATE",
				Bytes: caBytes,
			})

			caPrivKeyPEM := new(bytes.Buffer)
			pem.Encode(caPrivKeyPEM, &pem.Block{
				Type:  "RSA PRIVATE KEY",
				Bytes: x509.MarshalPKCS1PrivateKey(caPrivKey),
			})

			caCert = caPEM.Bytes()
			caKey = caPrivKeyPEM.Bytes()

			// write data
			err = ioutil.WriteFile(caBundlePath, caCert, 0600)
			if err != nil {
				return err
			}
			err = ioutil.WriteFile(caKeyPath, caKey, 0600)
			if err != nil {
				return err
			}
		} else {
			return fmt.Errorf("CA bundle file exists without key file")
		}
	} else {
		if _, err := os.Stat(caKeyPath); os.IsNotExist(err) {
			return fmt.Errorf("CA key file exists without bundle file")
		}

		caCert, err = ioutil.ReadFile(caBundlePath)
		if err != nil {
			return err
		}
		caKey, err = ioutil.ReadFile(caKeyPath)
		if err != nil {
			return err
		}
	}

	goproxyCa, err := tls.X509KeyPair(caCert, caKey)
	if err != nil {
		return err
	}
	if goproxyCa.Leaf, err = x509.ParseCertificate(goproxyCa.Certificate[0]); err != nil {
		return err
	}
	goproxy.GoproxyCa = goproxyCa
	goproxy.OkConnect = &goproxy.ConnectAction{Action: goproxy.ConnectAccept, TLSConfig: goproxy.TLSConfigFromCA(&goproxyCa)}
	goproxy.MitmConnect = &goproxy.ConnectAction{Action: goproxy.ConnectMitm, TLSConfig: goproxy.TLSConfigFromCA(&goproxyCa)}
	goproxy.HTTPMitmConnect = &goproxy.ConnectAction{Action: goproxy.ConnectHTTPMitm, TLSConfig: goproxy.TLSConfigFromCA(&goproxyCa)}
	goproxy.RejectConnect = &goproxy.ConnectAction{Action: goproxy.ConnectReject, TLSConfig: goproxy.TLSConfigFromCA(&goproxyCa)}
	return nil
}

func createProxy(addr string) {
	err := loadCAKeys()
	if err != nil {
		log.Fatal(err)
	}

	proxy := goproxy.NewProxyHttpServer()
	proxy.Logger = log.New(io.Discard, "", log.LstdFlags)
	proxy.OnRequest().HandleConnect(goproxy.AlwaysMitm)
	proxy.OnRequest().DoFunc(func(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) { // TODO: Move to onResponse for HTTP response codes
		body, _ := ioutil.ReadAll(req.Body)

		isAWSHostname, _ := regexp.MatchString(`^.*\.amazonaws\.com(?:\.cn)?$`, req.Host)
		if isAWSHostname {
			handleAWSRequest(req, body, 200)
		}

		req.Body = ioutil.NopCloser(bytes.NewBuffer(body))

		return req, nil
	})
	log.Fatal(http.ListenAndServe(addr, proxy))
}

type ServiceDefinition struct {
	Version    string                      `json:"version"`
	Metadata   ServiceDefinitionMetadata   `json:"metadata"`
	Operations map[string]ServiceOperation `json:"operations"`
	Shapes     map[string]ServiceStructure `json:"shapes"`
}

type ServiceOperation struct {
	Http   ServiceHttp      `json:"http"`
	Input  ServiceStructure `json:"input"`
	Output ServiceStructure `json:"output"`
}

type ServiceHttp struct {
	Method       string `json:"method"`
	RequestURI   string `json:"requestUri"`
	ResponseCode int    `json:"responseCode"`
}

type ServiceStructure struct {
	Shape        string                      `json:"shape"`
	Type         string                      `json:"type"`
	Member       *ServiceStructure           `json:"member"`
	Members      map[string]ServiceStructure `json:"members"`
	LocationName string                      `json:"locationName"`
	QueryName    string                      `json:"queryName"`
}

type ServiceDefinitionMetadata struct {
	APIVersion       string `json:"apiVersion"`
	EndpointPrefix   string `json:"endpointPrefix"`
	JSONVersion      string `json:"jsonVersion"`
	Protocol         string `json:"protocol"`
	ServiceFullName  string `json:"serviceFullName"`
	ServiceID        string `json:"serviceId"`
	SignatureVersion string `json:"signatureVersion"`
	TargetPrefix     string `json:"targetPrefix"`
	UID              string `json:"uid"`
}

func readServiceFiles() {
	files, err := serviceFiles.ReadDir("service")
	if err != nil {
		panic(err)
	}

	for _, dirEntry := range files {
		file, err := serviceFiles.Open("service/" + dirEntry.Name())
		if err != nil {
			panic(err)
		}

		data, err := ioutil.ReadAll(file)
		if err != nil {
			panic(err)
		}

		var def ServiceDefinition
		if json.Unmarshal(data, &def) != nil {
			panic(err)
		}

		serviceDefinitions = append(serviceDefinitions, def)
	}
}

func flatten(top bool, flatMap map[string][]string, nested interface{}, prefix string) error {
	assign := func(newKey string, v interface{}) error {
		switch v.(type) {
		case map[string]interface{}, []interface{}:
			if err := flatten(false, flatMap, v, newKey); err != nil {
				return err
			}
		default:
			flatMap[newKey] = append(flatMap[newKey], fmt.Sprintf("%v", v))
		}

		return nil
	}

	switch nested.(type) {
	case map[string]interface{}:
		for k, v := range nested.(map[string]interface{}) {
			if top {
				assign(k, v)
			} else {
				assign(prefix+"."+k, v)
			}
		}
	case []interface{}:
		for _, v := range nested.([]interface{}) {
			assign(prefix+"[]", v)
		}
	default:
		return fmt.Errorf("invalid object type")
	}

	return nil
}

func handleAWSRequest(req *http.Request, body []byte, respCode int) {
	host := req.Host
	uri := req.RequestURI

	var serviceDef ServiceDefinition
	hostSplit := strings.Split(host, ".")
	if hostSplit[len(hostSplit)-1] == "com" && hostSplit[len(hostSplit)-2] == "amazonaws" {
		endpointPrefix := hostSplit[len(hostSplit)-3]
		if len(hostSplit) > 3 {
			endpointPrefix = hostSplit[len(hostSplit)-4]
		}
		for _, serviceDefinition := range serviceDefinitions {
			if serviceDefinition.Metadata.EndpointPrefix == endpointPrefix { // TODO: Ensure latest version
				serviceDef = serviceDefinition
			}
		}
	} else {
		return
	}

	uriparams := make(map[string]string)
	params := make(map[string][]string)
	action := "*"

	if serviceDef.Metadata.Protocol == "rest-json" {
		// URL param schema
		urlobj, err := url.ParseRequestURI(uri)
		if err != nil {
			return
		}
		vals := urlobj.Query()

		// path part
		for operationName, operation := range serviceDef.Operations {
			templateMatches := regexp.MustCompile(`{([^/]+?)}`).FindAllStringSubmatch(operation.Http.RequestURI, -1)
			regexStr := fmt.Sprintf("^%s$", regexp.MustCompile(`{([^/]+?)}`).ReplaceAllString(operation.Http.RequestURI, "([^/]+)"))
			pathMatchSuccess := regexp.MustCompile(regexStr).Match([]byte(urlobj.Path))

			if operation.Http.Method == req.Method && pathMatchSuccess {
				action = operationName
				pathMatches := regexp.MustCompile(regexStr).FindAllStringSubmatch(urlobj.Path, -1)

				if len(pathMatches) > 0 && len(pathMatches) > 0 && len(templateMatches) == len(pathMatches[0])-1 {
					for i := 0; i < len(templateMatches); i++ {
						uriparams[templateMatches[i][1]] = pathMatches[0][1:][i]
					}
				}
			}
		}

		// query part
		for k, v := range vals {
			normalizedK := regexp.MustCompile(`\.member\.[0-9]+`).ReplaceAllString(k, "[]")
			normalizedK = regexp.MustCompile(`\.[0-9]+`).ReplaceAllString(normalizedK, "[]")

			resolvedPropertyName := resolvePropertyName(serviceDef.Operations[action].Input, normalizedK, "", "", serviceDef.Shapes)
			if resolvedPropertyName != "" {
				normalizedK = resolvedPropertyName
			}

			if len(params[normalizedK]) > 0 {
				params[normalizedK] = append(params[normalizedK], v...)
			} else {
				params[normalizedK] = v
			}
		}

		// body part
		if len(body) > 0 {
			var bodyJSON interface{}
			err := json.Unmarshal(body, &bodyJSON)
			if err != nil {
				return
			}

			flatten(true, params, bodyJSON, "")
		}
	} else if serviceDef.Metadata.Protocol == "json" {
		// JSON schema
		var bodyJSON interface{}
		err := json.Unmarshal(body, &bodyJSON)

		if err == nil {
			amzTargetHeader := req.Header.Get("X-Amz-Target")
			if amzTargetHeader != "" {
				action = strings.Split(amzTargetHeader, ".")[1]
				flatten(true, params, bodyJSON, "")
			} else {
				return
			}
		} else {
			return
		}
	} else if serviceDef.Metadata.Protocol == "ec2" || serviceDef.Metadata.Protocol == "query" {
		// URL param schema in body
		vals, err := url.ParseQuery(string(body))
		if err != nil {
			return
		}

		if len(vals["Action"]) != 1 || len(vals["Version"]) != 1 {
			return
		}
		action = vals["Action"][0]

		if serviceDef.Operations[action].Input.Type == "structure" {
			for k, v := range vals {
				if k != "Action" && k != "Version" {
					normalizedK := regexp.MustCompile(`\.member\.[0-9]+`).ReplaceAllString(k, "[]")
					normalizedK = regexp.MustCompile(`\.[0-9]+`).ReplaceAllString(normalizedK, "[]")

					resolvedPropertyName := resolvePropertyName(serviceDef.Operations[action].Input, normalizedK, "", "", serviceDef.Shapes)
					if resolvedPropertyName != "" {
						normalizedK = resolvedPropertyName
					}

					if len(params[normalizedK]) > 0 {
						params[normalizedK] = append(params[normalizedK], v...)
					} else {
						params[normalizedK] = v
					}
				}
			}
		}
	}

	region := "us-east-1"
	re, _ := regexp.Compile(`\.(.+)\.amazonaws\.com(?:\.cn)?$`)
	matches := re.FindStringSubmatch(host)
	if len(matches) == 2 {
		region = matches[1]
	}

	callLog = append(callLog, Entry{
		Region:              region,
		Type:                "ProxyCall",
		Service:             serviceDef.Metadata.ServiceID,
		Method:              action,
		Parameters:          params,
		URIParameters:       uriparams,
		FinalHTTPStatusCode: respCode,
	})

	handleLoggedCall()
}

func resolvePropertyName(obj ServiceStructure, searchProp string, path string, locationPath string, shapes map[string]ServiceStructure) (ret string) {
	if searchProp[len(searchProp)-2:] == "[]" { // trim trailing []
		searchProp = searchProp[:len(searchProp)-2]
	}

	if obj.Shape != "" {
		locationName := obj.LocationName
		queryName := obj.QueryName
		obj = shapes[obj.Shape]
		obj.LocationName = locationName
		obj.QueryName = queryName
	}

	switch obj.Type { // TODO: Exhaustive check for other types
	case "boolean", "timestamp", "blob", "map":
		return ""
	case "structure":
		for k, v := range obj.Members {
			newPath := fmt.Sprintf("%s.%s", path, k)
			if path == "" {
				newPath = k
			}

			newLocationPath := locationPath + "." + k
			if v.QueryName != "" {
				newLocationPath = locationPath + "." + v.QueryName
			} else if v.LocationName != "" {
				newLocationPath = locationPath + "." + v.LocationName
			}

			ret = resolvePropertyName(v, searchProp, newPath, newLocationPath, shapes)
			if ret != "" {
				return ret
			}
		}
	case "long", "float", "integer", "", "string":
		if len(locationPath) > 2 && locationPath[len(locationPath)-2:] == "[]" { // trim trailing []
			locationPath = locationPath[:len(locationPath)-2]
		}
		if len(locationPath) > 0 && locationPath[0] == '.' { // trim leading .
			locationPath = locationPath[1:]
		}

		if strings.ToLower(locationPath) == strings.ToLower(searchProp) {
			return path
		}
	case "list":
		newPath := fmt.Sprintf("%s[]", path)
		newLocationPath := fmt.Sprintf("%s[]", locationPath)

		ret = resolvePropertyName(*obj.Member, searchProp, newPath, newLocationPath, shapes)
		if ret != "" {
			return ret
		}
	}

	return ""
}
