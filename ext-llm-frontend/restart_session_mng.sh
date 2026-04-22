 docker stop  llm-chat
 docker rm llm-chat 
 docker rm llm-frontend
 docker build -t llm-frontend -f Dockerfile .
 docker run -d -p 8766:8766 --name llm-chat --add-host=host.docker.internal:host-gateway llm-frontend 