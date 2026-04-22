docker stop ext-llm-cipherer  
docker container rm  ext-llm-cipherer 
docker build -t ext-llm-cipherer .
docker run -d --link my-redis2:redis --name ext-llm-cipherer  -p 8211:8000 ext-llm-cipherer