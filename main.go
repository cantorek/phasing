package main

import (
	"bufio"
	"context"
	_ "embed"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"phasing/scripts"
	"regexp"
	"strconv"
	"syscall"

	"golang.org/x/crypto/ssh"

	"path/filepath"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"

	"github.com/manifoldco/promptui"
)

type Phasing struct {
	ServiceName    string
	Namespace      string
	Port           int
	LocalPort      int
	AgentLocalPort int
}

func (phasing *Phasing) RemoteEndpoint() string {
	return fmt.Sprintf("localhost:%d", phasing.Port)
}

func (phasing *Phasing) LocalEndpoint() string {
	return fmt.Sprintf("localhost:%d", phasing.LocalPort)
}

func (phasing *Phasing) AgentEndpoint() string {
	return fmt.Sprintf("localhost:%d", phasing.AgentLocalPort)
}

var phasing Phasing
var oldSelector map[string]string

func updateService(namespace, serviceName, kubeconfigPath string) (err error) {
	// Load the kubeconfig from the specified file
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		log.Fatalf("Error loading kubeconfig: %v", err)
	}

	// Create a Kubernetes clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Printf("Error creating Kubernetes clientset: %v", err)
		return err
	}

	// New selector and labels to set for the Service
	newSelector := map[string]string{"app": "phasing"}

	// Fetch the existing Service
	service, err := clientset.CoreV1().Services(namespace).Get(context.TODO(), serviceName, metav1.GetOptions{})
	if err != nil {
		log.Printf("Error fetching Service: %v\r\n", err)
		return err
	}

	// Print the current Service's port and target port
	// fmt.Printf("Service Port: %d\n", service.Spec.Ports[0].Port)
	phasing.Port = int(service.Spec.Ports[0].Port)
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
		log.Printf("Error patching Service: %v", retryErr)
		return retryErr
	}

	fmt.Printf("Service %s in namespace %s patched successfully.\r\n", serviceName, namespace)
	return nil
}

func selectService(namespace, kubeconfigPath string) (serviceName string, err error) {
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

	services, err := clientset.CoreV1().Services(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		panic(err.Error())
	}

	var serviceNames []string
	for _, svc := range services.Items {
		serviceNames = append(serviceNames, svc.Name)
	}

	prompt := promptui.Select{
		Label: "Select service to forward",
		Items: serviceNames,
		Size:  20,
	}

	idx, _, err := prompt.Run()
	if err != nil {
		return "", err
	}

	return serviceNames[idx], nil
}

func getCurrentNamespace(kubeconfigPath string) (string, error) {
	// Load the kubeconfig from the specified file
	config, err := clientcmd.LoadFromFile(kubeconfigPath)
	if err != nil {
		return "default", err
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
		log.Println(fmt.Sprintf("Cannot read SSH key file ", file))
		return nil
	}

	key, err := ssh.ParsePrivateKey(buffer)
	if err != nil {
		log.Println(fmt.Sprintf("Cannot parse SSH key file ", file))
		return nil
	}
	return ssh.PublicKeys(key)
}

// use kubectl to do port forwarding to remote phasing agent
// local port is selected by os (0)
// we need to use pipe for stdout parsing, because it's a long running - blocking - command
func PortForward() (err error) {
	cmd := exec.Command("kubectl", "port-forward", "--address=0.0.0.0", "pod/phasing", strconv.Itoa(phasing.AgentLocalPort)+":22")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	//this is super ugly, but hey! It works!
	go func() {
		reader := bufio.NewReader(stdout)
		for {
			line, err := reader.ReadString('\n')
			re := regexp.MustCompile(`Forwarding from 0\.0\.0\.0:(\d+) -> 22`)
			found := re.FindSubmatch([]byte(line))
			if len(found) > 0 {
				phasing.AgentLocalPort, err = strconv.Atoi(string(found[1]))
				return
			}
			if err != nil {
				log.Fatalln("Error regexping:", err)
				if err == io.EOF {
					break
				}
			}
		}
	}()

	err = cmd.Run()

	if err != nil {
		return fmt.Errorf("Error connecting to agent:", err)
	}

	return nil
}

func Init() (err error) {
	f, err := os.CreateTemp("", "phasing")
	if err != nil {
		fmt.Println("Error creating temporary file:", err)
		return fmt.Errorf("Error Initializing Phasing:", err)
	}

	defer os.Remove(f.Name())

	fyaml, err := os.CreateTemp("", "phasing")
	if err != nil {
		fmt.Println("Error creating temporary file:", err)
		return fmt.Errorf("Error Initializing Phasing:", err)
	}

	defer os.Remove(fyaml.Name())

	//write bash script
	err = os.WriteFile(f.Name(), []byte(scripts.InitScript), 0755)
	if err != nil {
		fmt.Println("Error writing to temporary file:", err)
		return fmt.Errorf("Error Initializing Phasing:", err)
	}

	// and now write a yaml file, omg, not very proud of this
	err = os.WriteFile(fyaml.Name(), []byte(scripts.PhasingYAML), 0644)
	if err != nil {
		fmt.Println("Error writing to temporary file:", err)
		return fmt.Errorf("Error Initializing Phasing:", err)
	}

	// Execute the Bash script using the "sh" command
	cmd := exec.Command("sh", f.Name(), fyaml.Name())
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err = cmd.Run()
	if err != nil {
		return fmt.Errorf("Error Initializing Phasing:", err)
	}

	return nil
}

func main() {

	var kubeconfigPath string
	var init bool

	kubeconfigPath = filepath.Join(os.Getenv("HOME"), ".kube", "config")
	currentNamespace, err := getCurrentNamespace(kubeconfigPath)
	if err != nil {
		currentNamespace = "default"
	}

	flag.StringVar(&phasing.ServiceName, "service", "phasing", "Service name")
	flag.IntVar(&phasing.LocalPort, "port", 7777, "Local port to forward remote service to")
	flag.StringVar(&phasing.Namespace, "namespace", currentNamespace, "Namespace name")
	flag.StringVar(&kubeconfigPath, "kubeconfig", kubeconfigPath, "Path to kube .config file")
	flag.BoolVar(&init, "init", false, "Run Phasing initialization")

	flag.Parse()

	args := flag.Args()

	if len(args) > 0 {
		phasing.ServiceName = args[0]
	}

	if len(args) > 1 {
		phasing.LocalPort, err = strconv.Atoi(args[1])
		if err != nil {
			log.Println("Please provide valid local port number", err)
			return
		}
	}

	if init {
		err := Init()
		if err != nil {
			log.Println("Could not initialize phasing", err)
		}
		return
	}

	go PortForward() // start port forwarding to agent in the background

	for phasing.AgentLocalPort == 0 {
		//if port is still 0, just sit and wait
	}

	if phasing.ServiceName == "phasing" {
		phasing.ServiceName, err = selectService(phasing.Namespace, kubeconfigPath)
	}

	for phasing.AgentLocalPort == 0 { // loop until port is set

	}

	// Create a channel to receive signals
	signalCh := make(chan os.Signal, 1)

	// Register for interrupt (Ctrl+C) and termination (SIGTERM) signals
	signal.Notify(signalCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		defer close(signalCh)
		select {
		case <-signalCh:
			updateService(phasing.Namespace, phasing.ServiceName, kubeconfigPath) // to restore old service config
			os.Exit(0)
		}
	}()

	err = updateService(phasing.Namespace, phasing.ServiceName, kubeconfigPath)
	if err != nil {
		log.Printf("Could not update service %s in %s\r\n", phasing.ServiceName, phasing.Namespace)
		return
	}
	defer updateService(phasing.Namespace, phasing.ServiceName, kubeconfigPath) // to restore old service config

	fmt.Printf("Starting phasing\r\nRemote endpoint is %s.%s:%d\r\nLocal endpoint is localhost:%d\r\n",
		phasing.ServiceName, phasing.Namespace, phasing.Port, phasing.LocalPort)

	sshConfig := &ssh.ClientConfig{
		User: "root",
		Auth: []ssh.AuthMethod{
			sshKeyFile(filepath.Join(os.Getenv("HOME"), ".ssh", "phasing_key")), // this is so misleading, omg
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	// Connect to remote agent server using agentEndpoint
	serverConn, err := ssh.Dial("tcp", phasing.AgentEndpoint(), sshConfig)
	if err != nil {
		log.Printf("Cannot connect to the remote agent. Make sure that Phasing has been initialized properly. %s \r\n", err)
		return
	}

	// Listen on remote server port
	listener, err := serverConn.Listen("tcp", phasing.RemoteEndpoint())
	if err != nil {
		log.Fatalln(fmt.Printf("Listen open port ON remote server error: %s", err))
	}
	defer listener.Close()

	// handle incoming connections
	for {
		// open a remote port
		remote, err := listener.Accept()
		if err != nil {
			if err == io.EOF {
				return
			}
			//log.Fatalln(err)
			log.Printf("Cannot setup remote endpoint. Make sure that Phasing has been initialized properly. %s \r\n", err)
			break
		}

		// Open a local connection
		local, err := net.Dial("tcp", phasing.LocalEndpoint())
		if err != nil {
			log.Println("Dial INTO local service error: ", err)
			//close remote connection given we couldn't fully establish the tunnel
			remote.Close()
		}

		// tunnel stuff between remote and local
		go tunnelTraffic(remote, local)
	}

}
