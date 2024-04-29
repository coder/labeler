.ONESHELL:


PROJECT := coder-labeler
DOCKER_TAG := us-central1-docker.pkg.dev/$(PROJECT)/labeler/labeler


.PHONY: build push deploy

build:
	# May need to run:
	#	gcloud auth configure-docker \
 	#	us-central1-docker.pkg.dev
	mkdir -p bin
	GOOS=linux GOARCH=amd64 go build -o bin/labeler ./cmd/labeler
	docker build -t $(DOCKER_TAG) .

push: build
	docker push $(DOCKER_TAG)

deploy: push
	# we keep CPU always allocated for background processing issue
	# indexing (WIP) and to eventually set labels outside of the
	# request-response cycle (escaping 10s webhook timeout)
	gcloud run deploy labeler --project $(PROJECT) --image $(DOCKER_TAG) --region us-central1 \
    --allow-unauthenticated --memory=512Mi \
	--min-instances=1 --no-cpu-throttling  \
	--set-secrets=OPENAI_API_KEY=openai-key:latest,GITHUB_APP_PEM=github-app-key:latest