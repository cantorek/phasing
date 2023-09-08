package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/crypto/ssh"

	"path/filepath"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
)

type Endpoint struct {
	Host string
	Port int
}

func (endpoint *Endpoint) String() string {
	return fmt.Sprintf("%s:%d", endpoint.Host, endpoint.Port)
}

// local service to be forwarded
var localEndpoint = Endpoint{
	Host: "localhost",
	Port: 7777,
}

// remote SSH server
var serverEndpoint = Endpoint{
	Host: "localhost",
	Port: 2222,
}

// remote forwarding port (on remote SSH server network)
var remoteEndpoint = Endpoint{
	Host: "localhost",
	Port: 7777,
}

var oldSelector map[string]string

func k8s(namespace, serviceName, kubeconfigPath string) {
	// Load the kubeconfig from the specified file
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		log.Fatalf("Error loading kubeconfig: %v", err)
	}

	// Create a Kubernetes clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("Error creating Kubernetes clientset: %v", err)
	}

	// New selector and labels to set for the Service
	newSelector := map[string]string{"app": "phasing"}

	// Fetch the existing Service
	service, err := clientset.CoreV1().Services(namespace).Get(context.TODO(), serviceName, metav1.GetOptions{})
	if err != nil {
		log.Fatalf("Error fetching Service: %v", err)
	}

	// Print the current Service's port and target port
	fmt.Printf("Service Port: %d\n", service.Spec.Ports[0].Port)
	remoteEndpoint.Port = int(service.Spec.Ports[0].Port)
	// fmt.Printf("Current Target Port: %d\n", service.Spec.Ports[0].TargetPort.IntVal)

	// Patch the Service's selector
	retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		service, err = clientset.CoreV1().Services(namespace).Get(context.TODO(), serviceName, metav1.GetOptions{})
		if err != nil {
			return err
		}

		// this will magically restore old service selector so we can defer the whole function from main
		//if old service selector not set
		if oldSelector == nil {
			//backup original service selector
			oldSelector = service.Spec.Selector
			// Update the selector
			service.Spec.Selector = newSelector
		} else {
			service.Spec.Selector = oldSelector
		}

		// Patch the Service
		_, updateErr := clientset.CoreV1().Services(namespace).Update(context.TODO(), service, metav1.UpdateOptions{})
		return updateErr
	})
	if retryErr != nil {
		log.Fatalf("Error patching Service: %v", retryErr)
	}

	fmt.Printf("Service %s in namespace %s patched successfully.\r\n", serviceName, namespace)
}

func getCurrentK8sNamespace(kubeconfigPath string) (string, error) {
	// Load the kubeconfig from the specified file
	config, err := clientcmd.LoadFromFile(kubeconfigPath)
	if err != nil {
		return "", err
	}

	// Get the current context name from the kubeconfig
	currentContextName := config.CurrentContext

	// Get the context for the current context name
	context := config.Contexts[currentContextName]

	// Retrieve the namespace from the context
	return context.Namespace, nil
}

func tunnelTraffic(remote, local net.Conn) {
	if remote == nil || local == nil {
		return
	}

	defer remote.Close()
	defer local.Close()
	chDone := make(chan bool)

	// Start local -> remote data transfer
	go func() {
		_, err := io.Copy(remote, local)
		if err != nil {
			log.Println(fmt.Sprintf("error while copy local->remote: %s", err))
		}
		chDone <- true
	}()

	// Start remote -> local data transfer
	go func() {
		_, err := io.Copy(local, remote)
		if err != nil {
			log.Println(fmt.Sprintf("error while copy remote->local: %s", err))
		}
		chDone <- true
	}()

	<-chDone
}

func sshKeyFile(file string) ssh.AuthMethod {
	buffer, err := os.ReadFile(file)
	if err != nil {
		log.Fatalln(fmt.Sprintf("Cannot read SSH key file %s", file))
		return nil
	}

	key, err := ssh.ParsePrivateKey(buffer)
	if err != nil {
		log.Fatalln(fmt.Sprintf("Cannot parse SSH key file %s", file))
		return nil
	}
	return ssh.PublicKeys(key)
}

func main() {

	var serviceName string
	var namespace string
	var kubeconfigPath string

	kubeconfigPath = filepath.Join(os.Getenv("HOME"), ".kube", "config")
	namespace, err := getCurrentK8sNamespace(kubeconfigPath)

	flag.StringVar(&serviceName, "service", "phasing", "Service name")
	flag.StringVar(&namespace, "namespace", namespace, "Namespace name")
	flag.StringVar(&kubeconfigPath, "kubeconfig", kubeconfigPath, "Path to kube .config file")

	flag.Parse()

	args := flag.Args()

	if len(args) > 0 {
		serviceName = args[0]
	}

	// Create a channel to receive signals
	signalCh := make(chan os.Signal, 1)

	// Register for interrupt (Ctrl+C) and termination (SIGTERM) signals
	signal.Notify(signalCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		defer close(signalCh)
		select {
		case <-signalCh:
			k8s(namespace, serviceName, kubeconfigPath) // to restore old service config
			os.Exit(0)
		}
	}()

	k8s(namespace, serviceName, kubeconfigPath)
	defer k8s(namespace, serviceName, kubeconfigPath) // to restore old service config

	fmt.Printf("Starting tunnel - Remote endpoint is %s.%s:%d local endpoint is %s\r\n",
		serviceName, namespace, remoteEndpoint.Port, localEndpoint.String())

	// refer to https://godoc.org/golang.org/x/crypto/ssh for other authentication types
	sshConfig := &ssh.ClientConfig{
		User: "root",
		Auth: []ssh.AuthMethod{
			sshKeyFile(filepath.Join(os.Getenv("HOME"), ".ssh", "phasing_key")), // this is so misleading, omg
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	// Connect to SSH remote server using serverEndpoint
	serverConn, err := ssh.Dial("tcp", serverEndpoint.String(), sshConfig)
	if err != nil {
		log.Printf("Cannot connect to the remote agent. Make sure that Phasing has been initialized properly. %s \r\n", err)
		return
	}

	// Listen on remote server port
	listener, err := serverConn.Listen("tcp", remoteEndpoint.String())
	if err != nil {
		log.Fatalln(fmt.Printf("Listen open port ON remote server error: %s", err))
	}
	defer listener.Close()

	// handle incoming connections
	for {
		// open a remote port
		remote, err := listener.Accept()
		if err != nil {
			//log.Fatalln(err)
			log.Printf("Cannot setup remote endpoint. Make sure that Phasing has been initialized properly. %s \r\n", err)
			break
		}

		// Open a local connection
		local, err := net.Dial("tcp", localEndpoint.String())
		if err != nil {
			log.Println("Dial INTO local service error: %s", err)
			//close remote connection given we couldn't fully establish the tunnel
			remote.Close()
		}

		// tunnel stuff between remote and local
		go tunnelTraffic(remote, local)
	}

}
