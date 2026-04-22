cd ..
 docker stop  ext-llm-gateway
 docker container rm  ext-llm-gateway
 docker build -t ext-llm-gateway -f ext-llm-gateway/Dockerfile .
 docker run -d  --link my-redis2:redis --name ext-llm-gateway  -p 7700:7700 ext-llm-gateway
