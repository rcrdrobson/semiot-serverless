package main

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"https://github.com/rcrdrobson/semiot-serverless/src/docker"
)

const poolSize = 10

var poolMap = make(map[string][]string)

var (
	dockerClient    = docker.Client{}
	serviceNameHost = "localhost"
	serviceNamePort = "9090"
)

const (
	serverlessEndpoint = "/serverless/"
	callEndpoint       = "/call/"
	port               = "9096"
)

func main() {
	fmt.Println("Starting SL-HANDLER at port: '" + port + "'")
	fmt.Println("Importants:")
	fmt.Println("serviceNameHost: " + serviceNameHost)
	fmt.Println("serviceNamePort: " + serviceNamePort)

	fmt.Println("Starting Docker Client connection")

	dockerClient.Init()
	if isConnected := dockerClient.IsConnected(); !isConnected {
		fmt.Println("Failed to connect Docker client")
		return
	}
	fmt.Println("Connected in Docker Client")

	http.HandleFunc(serverlessEndpoint, serverless)
	http.HandleFunc(callEndpoint, call)

	fmt.Println("Started SL-HANDLER")

	http.ListenAndServe(":"+port, nil)
}

func serverless(res http.ResponseWriter, req *http.Request) {
	fmt.Println("Serverless func")
	var body string
	switch req.Method {
	case "POST":
		body = "Method POST detected"
		fmt.Println(body)
		serverlessPost(res, req)
	default:
		body = "Method not allowed"
		fmt.Println(body)
		http.Error(res, body, http.StatusMethodNotAllowed)
	}
}

func serverlessPost(res http.ResponseWriter, req *http.Request) {
	fmt.Println("ServerlessPost func")
	name, code, dockerFile, port := extractServerlessData(res, req.Body)

	fmt.Println("Creating Docker Image to Serverless")
	err := dockerClient.CreateImage(
		name,
		docker.FileInfo{Name: "Dockerfile", Text: dockerFile},
		docker.FileInfo{Name: "code.go", Text: code},
	)
	if err == nil {
		fmt.Println("Docker Image has created")

		fmt.Println("Creating Containers Pool with size " + strconv.FormatInt(poolSize, 10) + "")

		var wg sync.WaitGroup
		wg.Add(poolSize)

		for i := 0; i < poolSize; i++ {
			go func(i int, name string) {
				defer wg.Done()
				containerID, errCreateContainer := dockerClient.CreateContainer(name)

				if errCreateContainer != nil {
					fmt.Println("Error to CREATE the container number '" + strconv.Itoa(i) + "'")
				} else {
					containerIP, errStartContainer := dockerClient.StartContainer(containerID)
					if errStartContainer != nil {
						fmt.Println("Error to START the container number '" + strconv.Itoa(i) + "'")
					} else {
						poolMap[name] = append(poolMap[name], containerIP)
					}
				}
			}(i, name)
		}

		wg.Wait()
		fmt.Println("Containers Pool has created with size " + strconv.Itoa(poolSize))

		fmt.Println("Binding container pool at server name and exposed in port " + port)
		err := serviceNameBind(name, port)
		if err == nil {
			body := fmt.Sprintf("Function Created at %v%v\n", req.RequestURI, name)
			fmt.Println("Serverr error response body: " + body)
			res.Write([]byte(body))
			res.WriteHeader(http.StatusCreated)
		} else {
			body := fmt.Sprintf("Function Created at %v%v\nBut fail to bind in service name", req.RequestURI, name)
			fmt.Println("Serverr error response body: " + body)
			res.Write([]byte(body))
			res.WriteHeader(http.StatusInternalServerError)
		}
	} else {
		body := "ERROR: Docker Image not created:\n" + err.Error()
		fmt.Println("Serverr error response body: " + body)
		res.Write([]byte(body))
		res.WriteHeader(http.StatusCreated)
	}

}

func extractServerlessData(res http.ResponseWriter, jsonBodyReq io.Reader) (name, code, dockerFile, port string) {
	fmt.Println("ExtractServerlessData func")
	var jsonBody interface{}
	err := json.NewDecoder(jsonBodyReq).Decode(&jsonBody)
	if err != nil {
		fmt.Println("ExtractServerlessData return error: \n" + err.Error())
		fmt.Println(err)
		http.Error(res, err.Error(), 500)
		return
	}

	var bodyData = jsonBody.(map[string]interface{})
	fmt.Println("ExtractServerlessData return json: ")
	fmt.Println(bodyData)
	return bodyData["name"].(string), bodyData["code"].(string), bodyData["dockerFile"].(string), bodyData["port"].(string)
}

func call(res http.ResponseWriter, req *http.Request) {
	fmt.Println("Call func")
	requestData := req.RequestURI[6:]
	slashIndex := strings.Index(requestData, "/")
	if slashIndex == -1 {
		body := "Serverless endpoint '" + requestData + "'not provided"
		fmt.Println("Call func error: " + body)

		res.WriteHeader(http.StatusNotFound)
		res.Write([]byte(body))
		return
	}

	imageName := requestData[:slashIndex]

	containerIP := poolMap[imageName][0]

	poolMap[imageName] = poolMap[imageName][1:]
	poolMap[imageName] = append(poolMap[imageName], containerIP)

	fmt.Println("Calling te Container with intern IP '" + containerIP + "' and name '" + imageName + "'")
	gatewayReq, err := http.NewRequest(req.Method, fmt.Sprintf("http://%v:8080/%v", containerIP, requestData[len(imageName)+1:]), req.Body)
	if err != nil {
		body := "Request error: \n" + err.Error()
		fmt.Println("Call func error: " + body)

		res.WriteHeader(http.StatusNotFound)
		res.Write([]byte(body))
		return
	}
	var gatewayRes *http.Response
	var i int
	limitTime := 2000
	for i = 0; i < limitTime; i++ {
		gatewayRes, err = http.DefaultClient.Do(gatewayReq)
		fmt.Println(err)
		if err == nil {
			fmt.Printf("Connection with '%s' is OK", imageName)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if i == limitTime {
		body := "Time out in sl-handler"
		fmt.Printf(body)
		res.WriteHeader(http.StatusInternalServerError)
		res.Write([]byte(body))
		return
	}
	applicationCode := gatewayRes.StatusCode
	applicationBody, _ := ioutil.ReadAll(gatewayRes.Body)
	fmt.Println(applicationBody)
	fmt.Println("Call response " + string(applicationCode))

	res.WriteHeader(applicationCode)
	res.Write(applicationBody)
}

func serviceNameBind(name, portExport string) (err error) {
	fmt.Println("ServiceNameBind func")
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return err
	}
	var currentIP string
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				currentIP = ipnet.IP.String()
			}
		}
	}

	fmt.Println("Json to be bind:")

	jsonBody := "{\"name\":\"" + name + "\",\"hostRunner\": \"" + currentIP + "\",\"portRunner\": \"" + port + "\",\"port\": \"" + portExport + "\"}"
	var body io.Reader
	body = strings.NewReader(jsonBody)

	fmt.Println("Json to be bind:")
	fmt.Println(body)

	req, err2 := http.NewRequest("POST", fmt.Sprintf("http://%v:%v/bind/", serviceNameHost, serviceNamePort), body)
	if err2 != nil {
		return err
	}

	fmt.Println("Try bind container '" + name + "'")
	fmt.Println(body)
	var err3 error
	timeLimit := 2000
	for i := 0; i < timeLimit; i++ {
		_, err3 = http.DefaultClient.Do(req)
		fmt.Println(err3)
		if err3 == nil {
			fmt.Printf("Connection with Service Name is OK")
			fmt.Println("Container has binded")
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if timeLimit >= 2000 {
		return err3
	}

	return nil
}
