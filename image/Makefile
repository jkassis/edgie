IMAGE_NAME = edgie
NAMESPACE= fg
REGISTRY_HOST = asdf

.PHONY: build
build:
	docker build -t $(IMAGE_NAME) .

.PHONY: push
push:
	docker tag $(IMAGE_NAME):latest $(REGISTRY_HOST)/$(NAMESPACE)/$(IMAGE_NAME):latest
	docker login -u kubeadmin -p (oc whoami -t) $(REGISTRY_HOST)
	docker push $(REGISTRY_HOST)/$(NAMESPACE)/$(IMAGE_NAME):latest
