MKFILE_PATH := $(abspath $(lastword $(MAKEFILE_LIST)))
PROJECT_PATH := $(patsubst %/,%,$(dir $(MKFILE_PATH)))
.DEFAULT_GOAL := help
.PHONY: build e2e licenses-check verify-manifest push-manifest test-crds
UNAME := $(shell uname)

ifeq (${UNAME}, Linux)
  SED=sed
else ifeq (${UNAME}, Darwin)
  SED=gsed
endif

OPERATORCOURIER := $(shell command -v operator-courier 2> /dev/null)
LICENSEFINDERBINARY := $(shell command -v license_finder 2> /dev/null)
DEPENDENCY_DECISION_FILE = $(PROJECT_PATH)/doc/dependency_decisions.yml
MANIFEST_RELEASE ?= 1.0.$(shell git rev-list --count master)
APPLICATION_REPOSITORY_NAMESPACE ?= apicastoperatormaster

help: Makefile
	@sed -n 's/^##//p' $<

## vendor: Populate vendor directory
vendor:
	GO111MODULE=on go mod vendor

IMAGE ?= quay.io/3scale/apicast-operator
SOURCE_VERSION ?= master
VERSION ?= v0.0.1
NAMESPACE ?= $(shell kubectl config view --minify -o jsonpath='{.contexts[0].context.namespace}' 2>/dev/null || echo operator-test)
OPERATOR_NAME ?= apicast-operator

## build: Build operator
build: vendor
	operator-sdk build $(IMAGE):$(VERSION)

## push: push operator docker image to remote repo
push:
	docker push $(IMAGE):$(VERSION)

## pull: pull operator docker image from remote repo
pull:
	docker pull $(IMAGE):$(VERSION)

tag:
	docker tag $(IMAGE):$(SOURCE_VERSION) $(IMAGE):$(VERSION)

## local: push operator docker image to remote repo
local:
	OPERATOR_NAME=$(OPERATOR_NAME) operator-sdk up local --namespace $(NAMESPACE)

## e2e-setup: create OCP project for the operator
e2e-setup:
	kubectl create namespace $(NAMESPACE)

## e2e-local-run: running operator locally with go run instead of as an image in the cluster
e2e-local-run:
	OPERATOR_NAME=$(OPERATOR_NAME) operator-sdk test local ./test/e2e --up-local --namespace $(NAMESPACE) --go-test-flags '-v -timeout 0'

## e2e-run: operator local test
e2e-run:
	operator-sdk test local ./test/e2e --go-test-flags '-v -timeout 0' --debug --image $(IMAGE) --namespace $(NAMESPACE)

## e2e-clean: delete operator OCP project
e2e-clean:
	kubectl delete namespace --force $(NAMESPACE) || true

## e2e: e2e-clean e2e-setup e2e-run
e2e: e2e-clean e2e-setup e2e-run

## licenses.xml: Generate licenses.xml file
licenses.xml:
ifndef LICENSEFINDERBINARY
	$(error "license-finder is not available please install: gem install license_finder --version 5.7.1")
endif
	license_finder report --decisions-file=$(DEPENDENCY_DECISION_FILE) --quiet --format=xml > licenses.xml

## licenses-check: Check license compliance of dependencies
licenses-check: vendor
ifndef LICENSEFINDERBINARY
	$(error "license-finder is not available please install: gem install license_finder --version 5.7.1")
endif
	@echo "Checking license compliance"
	license_finder --decisions-file=$(DEPENDENCY_DECISION_FILE)

## verify-manifest: Test manifests have expected format
verify-manifest:
ifndef OPERATORCOURIER
	$(error "operator-courier is not available please install pip3 install operator-courier")
endif
	cd $(PROJECT_PATH)/deploy/olm-catalog && operator-courier verify --ui_validate_io apicast-operator/

## test-crds: Run CRD unittests
test-crds: vendor
	cd $(PROJECT_PATH)/test/crds && go test -v

## push-manifest: Push manifests to application repository
push-manifest:
ifndef OPERATORCOURIER
	$(error "operator-courier is not available please install pip3 install operator-courier")
endif
	cd $(PROJECT_PATH)/deploy/olm-catalog && operator-courier push apicast-operator/ $(APPLICATION_REPOSITORY_NAMESPACE) apicast-operator-master $(MANIFEST_RELEASE) "$(TOKEN)"
