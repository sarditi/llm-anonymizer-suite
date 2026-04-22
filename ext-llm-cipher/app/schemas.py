from pydantic import BaseModel
from typing import List, Optional, Any
from datetime import datetime

class Content(BaseModel):
    input_text: str
    attachments: List[Any] = []

class APIRequest(BaseModel):
    external_chat_id: str
    internal_chat_id: str
    llm: str
    created_at: datetime
    content: Content

class APIResponse(BaseModel):
    external_chat_id: str
    internal_chat_id: str
    llm: str
    created_at: datetime
    content: Content

class ReqRedisAccessPermissions(BaseModel):
    user_id: str
    pwd: str
    redis_url: str
    read_content_meta: Optional[str] = None
    write_content_meta: Optional[str] = None
    keys_allowed_read_access: Optional[List[str]] = None
    keys_allowed_write_access: Optional[List[str]] = None

class CipherResponse(BaseModel):
    message: str
    


