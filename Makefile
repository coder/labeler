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
	gcloud run deploy labeler --project $(PROJECT) --image $(DOCKER_TAG) --region us-central1 \
    --allow-unauthenticated --memory=128Mi --min-instances=0 \
	--set-secrets=OPENAI_API_KEY=openai-key:latest,GITHUB_APP_PEM=github-app-key:latest