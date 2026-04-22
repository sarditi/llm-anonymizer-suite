from .schemas import APIRequest, APIResponse, ReqRedisAccessPermissions
from app.utils.logger import logger

def process_request(request: APIRequest) -> APIResponse:
    logger.info("Received request : " + request.model_dump_json(indent=2))

    response = APIResponse(
        external_chat_id=request.external_chat_id,
        internal_chat_id=request.internal_chat_id,
        llm=request.llm,
        created_at=request.created_at,
        content=request.content
    )

    print("Sending response:")
    print(response.model_dump_json(indent=2))
    return response

