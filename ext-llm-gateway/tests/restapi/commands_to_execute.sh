curl -X POST -H "Content-Type: application/json" \
-d @/Users/sagi.arditi/gptanonym/llm-anonymizer-suite/ext-llm-gateway/tests/restapi/set_req3_existing_chtid.json \
http://localhost:7700/set_req_from_adapter

curl -X POST -H "Content-Type: application/json" \
-d @/Users/sagi.arditi/gptanonym/llm-anonymizer-suite/ext-llm-gateway/tests/restapi/set_req2_existing_chtid.json \
http://localhost:7700/set_req_from_adapter

curl -X POST -H "Content-Type: application/json" \
-d @/Users/sagi.arditi/gptanonym/llm-anonymizer-suite/ext-llm-gateway/tests/restapi/set_req_existing_chtid.json \
http://localhost:7700/set_req_from_adapter

curl -X POST -H "Content-Type: application/json" \
-d @/Users/sagi.arditi/gptanonym/llm-anonymizer-suite/ext-llm-gateway/tests/restapi/initchat.json \
http://localhost:7700/init_chat