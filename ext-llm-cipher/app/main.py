from fastapi import FastAPI,Request, Header
from app.schemas import APIRequest, APIResponse, ReqRedisAccessPermissions, Content
from app.process_req import process_request
#from app.redisacl import process_redis_content, process_redis_content_test
from app.redisacl import RedisCipherManager # Import the new class
from fastapi.responses import JSONResponse
from http import HTTPStatus
from app.utils.logger import logger
from typing import Annotated

app = FastAPI(title="Cipher/Decipher API")
cipher_manager = RedisCipherManager()

@app.post("/decipher")
async def decipher(
    request: ReqRedisAccessPermissions, 
    request_hd: Request,
    x_user_id: Annotated[str | None, Header()] = None
):
    logger.debug("/decipher was called...")
    
    # Use the header value from our Annotated parameter or fall back to request_hd
    chat_id = x_user_id or request_hd.headers.get("X-User-ID")
    
    if not chat_id:
        return JSONResponse(
            content="X-User-ID is missing or empty",
            status_code=HTTPStatus.BAD_REQUEST
        )

    #success, message = cipher_manager.process_redis_content_old(request, chat_id)
    success, message = cipher_manager.decipher(request)
    
    if success:
        return {
            "message": f"chat_id: {chat_id} - Cipher completed successfully"
        }
    else:
        logger.error(f"chat_id: {chat_id} - {message}")
        return JSONResponse(
            content=f"chat_id: {chat_id} - {message}",
            status_code=HTTPStatus.INTERNAL_SERVER_ERROR
        )


@app.post("/cipher")
async def cipher(
    request: ReqRedisAccessPermissions, 
    request_hd: Request,
    x_user_id: Annotated[str | None, Header()] = None
):
    logger.debug("/cipher was called...")
    
    # Use the header value from our Annotated parameter or fall back to request_hd
    chat_id = x_user_id or request_hd.headers.get("X-User-ID")
    
    if not chat_id:
        return JSONResponse(
            content="X-User-ID is missing or empty",
            status_code=HTTPStatus.BAD_REQUEST
        )

    success, message = cipher_manager.cipher(request, chat_id)
    
    if success:
        logger.debug(f"Cipher succeeded chat_id: {chat_id} - {message}")
        return {
            "message": f"chat_id: {chat_id} - Cipher completed successfully"
        }
    else:
        logger.error(f"chat_id: {chat_id} - {message}")
        return JSONResponse(
            content=f"chat_id: {chat_id} - {message}",
            status_code=HTTPStatus.INTERNAL_SERVER_ERROR
        )
