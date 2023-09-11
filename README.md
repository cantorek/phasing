# Phasing

**Description**: Redirect traffic from remote kubernetes services to your laptop.

[![GitHub License](https://img.shields.io/github/license/cantorek/phasing)](LICENSE)
[![GitHub Stars](https://img.shields.io/github/stars/cantorek/phasing)](https://github.com/cantorek/phasing/stargazers)
[![GitHub Issues](https://img.shields.io/github/issues/cantorek/phasing)](https://github.com/cantorek/phasing/issues)

## Table of Contents

- [Project Overview](#project-overview)
- [Getting Started](#getting-started)
- [Prerequisites](#prerequisites)
- [Usage](#usage)

## Project Overview

Phasing, enables seamless traffic redirection from cloud-based Kubernetes services right to your local laptop. Revolutionize your microservices development experience by crafting and executing application containers on your local machine, all while your entire stack thrives in the cloud.

## Getting Started

Download one of the releases here - https://github.com/cantorek/phasing/releases

`./phasing -init`

`./phasing`

## Prerequisites

`kubectl` needs to be in your PATH

You need to be logged in to your kubernetes cluster and able to run stuff like `kubectl get pods` and create pods in current context.

## Usage
```
Usage of ./phasing:
  -init
        Run initialization
  -kubeconfig string
        Path to kube .config file (default "/home/canto/.kube/config")
  -namespace string
        Namespace name (current namespace by default)
  -port int
        Local port to forward remote service to (default 7777)
  -service string
        Service name (default "phasing")

```

**PRO TIP**
You can also do short args
```
./phasing SERVICE
```
or
```
./phasing SERVICE PORT
```