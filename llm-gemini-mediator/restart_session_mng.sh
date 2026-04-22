 cd ..
 docker stop  llm-gemini-mediator
 docker container rm  llm-gemini-mediator
 docker build --no-cache -t llm-gemini-mediator -f llm-gemini-mediator/Dockerfile .
 docker run -e GEMINI_MODEL="gemini-2.5-flash" -e "GEMINI_PROVIDER=SIMPLE_GEMINI" -e GEMINI_API_KEY="$GEMINI_API_KEY" -d  --link my-redis2:redis --name llm-gemini-mediator  -p 9111:9111 -p 6060:6060 llm-gemini-mediator
