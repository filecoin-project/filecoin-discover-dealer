.PHONY: $(MAKECMDGOALS)

build: webapi cron

mkbin:
	@mkdir -p bin/

webapi: mkbin
	go build -o bin/dd-webapi ./webapi

cron: mkbin
	go build -o bin/dd-cron ./cron
