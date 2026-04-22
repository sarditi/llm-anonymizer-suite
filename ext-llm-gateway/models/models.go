package models

import "time"

//Unique identifier of the pod
var ProcessID string
/*
 Content holds the content sent to the worker groups. Note that all fields are defined as omitempty, that way any other types 
 of contents that might be requireed in the future (integrating new LLM etc.) will not cause a regression for other dispatchers 
*/
type Content struct {
	InputText    string        `json:"input_text,omitempty"`
	InlineDatas  []InlineData  `json:"inline_datas,omitempty"`
	FileDatas    []FileData    `json:"file_datas,omitempty"`
    Thought      bool          `json:"thought,omitempty"`   
	Explanations string        `json:"explanations,omitempty"`
}
/*
	For future use - TBD
*/
type Blob struct {
    MIMEType string `json:"mime_type"` // e.g., "image/jpeg"
    Data     []byte `json:"data"`      // The actual byte slice
}
/*
	For future use - TBD
*/
type InlineData struct {
    Text       string      `json:"text" binding:"required"`      
    IsBase64   bool        `json:"is_base64" binding:"required"`      
	Data       *Blob       `json:"inline_data" binding:"required"` 
}
/*
	For future use - TBD
*/
type FileData struct {
    Text       string      `json:"text" binding:"required"`      
    DataUri    *FileData   `json:"file_data" binding:"required"` 
}
/*
	Initialized a request - a required step to initialize interaction with the worker group. 
	Since security has yet to be implemented (Authentication/Authorization) the fileds have litte meaning
*/
type InitChatRequest struct {
	LLM                    string     `json:"llm,omitempty"`
	CreatedAt              *time.Time `json:"created_at,omitempty"`
	UserID                 string     `json:"user_id,omitempty"`
	PersistanceTimeSeconds int        `json:"persist_time_second,omitempty"`
	AdapterID              string     `json:"adapter_id,omitempty"`
}
/*
	Response for InitChatRequest
*/
type InitChatResponse struct {
	Status  	  string `json:"status"` // success upon successful initiation
	ChatID  	  string `json:"chat_id"` // Unique identifier of the chat id, must be used when start to set requests (i.e. SetRequest)
	SetNextReqKey string `json:"set_next_req_key"` // Unique identifier of the first sequence. Must be usedd when setting request for the first time after initiation
	Message 	  string `json:"message"`
	/*
	  The password to the stream in which the adapter that initiated the request can asynchronously read the reponse once the worker group is finished its processing. 
	  The stream will ne in redis synch and will be name STREAM_<the related chat id>. The username is the adapter id provided in InitChatRequest
	*/
	StreamPwd     string `json:"stream_pwd"` 
}
/*
	For future use - TBD
*/
type Attachment struct {
	Filename          string `json:"filename"`
	Filetype          string `json:"filetype"` // "doc" or "excel"
	FileContentBase64 string `json:"file_content_base64"`
}
/*
	Set a request to the chat id with the an allowed sequence key which was provide either by InitChatResponse or by previous successful request
	The response it self is stored in synch db stream STREAM_<chat id> for which the adapter that initiated the process with InitChatRequest got on
	the response the stream paswword to retrieve the content (see InitChatResponse)
*/
type SetRequest struct {
	ReqKey    	   string `json:"request_key" binding:"required"`
	InternalChatID string  `json:"internal_chat_id"  binding:"required"`
	Content        Content `json:"content" binding:"required"`
	StreamPwd     string   `json:"stream_pwd,omitempty"`
}
/*
	Response the adapter should expect from STREAM_<chat id>, where the worker group finished processing a request.
	Note that ReqRedisAccessPermissions and ErrorString are omitempty. ErrorString is omitempty since if the process is successful we do not expect it to be
	populated.
	ReqRedisAccessPermissions is also omitempty simply because this is the CURRENT implementation of the final response.
	We could have different dispatchers/flow which the response will be different... the current implementation provides the Redis DATASOURCE url , the 
	key which holds the content and the credentioals, so when the adapter reads the rsponse it can access the content selectively.
*/
type GetResponseFromStream struct {
	ReqRedisAccessPermissions  *ReqRedisAccessPermissions `json:"req_redis_access_permissions,omitempty"`
	ErrorString                 string 					 `json:"error_string,omitempty"`
}
/*
	See GetResponseFromStream, this is how data is tranferred between dispatcher (IN THE CURRENT IMPLEMENTATION). This is also the format of the final messahe
	in STREAM_<chat id> for the adapter.
	As described above, the current implementation provides the Redis DATASOURCE url , the 
	key which holds the content and the credentioals, so when the adapter reads the response it can access the content selectively.
*/
type ReqRedisAccessPermissions struct {
	UserID  	    		string   `json:"user_id" binding:"required"`
	Pwd      	    		string   `json:"pwd" binding:"required"`
	RedisURL  	    		string   `json:"redis_url" binding:"required"`
	ReadAccessContentMeta 	string `json:"read_content_meta,omitempty"`
	WriteAccessContentMeta	string `json:"write_content_meta,omitempty"`
	ReadAccessKeys  		[]string `json:"keys_allowed_read_access,omitempty"`
	WriteAccessKeys 		[]string `json:"keys_allowed_write_access,omitempty"`
}
/*
  This response is relevant for the handler endpoint set_req_from_adapter_sync which provides a synchronized answer
  This endpoint is setup for a simple UI interaction, in different user groups concerning heavy content we might need to eliminate that option
  as response time could be 10s of minutes.
*/
type SetReqHttpSyncResponse struct {
	Status         string `json:"status" binding:"required"`
	ChatID         string `json:"chat_id" binding:"required"`
	SetNextReqKey  string `json:"set_next_req_key,omitempty"`
	Content        Content `json:"content,omitempty"`
	Message        string  `json:"message,omitempty"`
}
/*
 	Response from SetRequest, indicating success or failed action 
*/
type SetReqHttpResponse struct {
	Status  string `json:"status" binding:"required"`
	ChatID  string `json:"chat_id" binding:"required"`
	Message string `json:"message" binding:"required"`
}
/*
	Structure representing the configuration, this implementation has a config inline (see conf/config.json)
*/
type LlmExtGwConfig struct { 
	SynchDB              DataSourceConfig  `json:"synchdb" binding:"required"`              
	Datasource           DataSourceConfig  `json:"datasource" binding:"required"`              
	Handler           	 Handler           `json:"handler" binding:"required"`              
	WorkerGroup  		 Workergroup       `json:"workergroup" binding:"required"`
}
/*
	Configuration of the data source. Note that apart of the name all fields are omitempty as we can expect more data sources implementations that might 
	require additional fields. Note that the relevant data source will be loaded (reflection style) from the registry/registry.go. 
*/
type DataSourceConfig struct {
    // Network address host:port.
	Name  string `json:"name" binding:"required"`
	Addr string `json:"addr,omitempty"`
    Username string `json:"username,omitempty"`
    Password string `json:"password,omitempty"` //Will include in our case the envronment variable name pointing to the file with the secret

    // Database to be selected after connecting to the server.
    DB int `json:"db,omitempty"`

    // Pool management
    PoolSize     int `json:"pool_size,omitempty"`
    MinIdleConns int `json:"min_idle_conns,omitempty"`

    // Timeouts (represented as integers for JSON simplicity)
    // Note: You will need to convert these to time.Duration in your code
    ConnMaxIdleTime int `json:"conn_max_idle_time_seconds,omitempty"`
    ConnMaxLifetime int `json:"conn_max_lifetime_seconds,omitempty"`
    ReadTimeout     int `json:"read_timeout_ms,omitempty"`
    WriteTimeout    int `json:"write_timeout_ms,omitempty"`
}
/*
	See comment for LlmExtGwConfig and related config file conf/config.json for reference
*/
type Workergroup struct {
	ValRedisMaxSizeInMB  string  `json:"max_size_in_mb_of_val_in_redis" binding:"required"`
	ValRedisMaxSizeByte int
    WorkerGroupName 	string   `json:"workergroup" binding:"required"`
	Workers  			[]Worker `json:"workers" binding:"required"`
	Prompt      		*string  `json:"prompt,omitempty"`
	SessionTTLSec       int 	 `json:"session_ttl_sec,omitempty"`
}
/*
	Configuration of the handler, the handler is the access point to the system, the current implemetation relies on gin http server 
	Note that the relevant data source will be loaded (reflection style) from the registry/registry.go. 
	Note that apart of the name all fields are omitempty as we can expect more handlers implementations that might 
	require additional fields (e.g. gRPC). Note that the relevant handler will be loaded (reflection style) from the registry/registry.go. 
*/
type Handler struct {
	Name            string `json:"name" binding:"required"`
	Port            string `json:"port,omitempty"`
	Adapters      []string `json:"adapters" binding:"required"`   
}
/*
	Configuration of the Dispatcher, the Dispatchers are the working units of the worker group, currently we have 2 dispatchers (se in registry/registry.go) 
	Note that the relevant dispatcher per worker will be loaded (reflection style) from the registry/registry.go. 
	Note that apart of the name all fields are omitempty as we can expect more dispatchers implementations that might 
	require additional fields. Note that the relevant dispatcher will be loaded (reflection style) from the registry/registry.go. 
*/
type Dispatcher struct {
	Protocol          string `json:"protocol" binding:"required"`
	URL               string `json:"url,omitempty"`
	DatasourceUrl     string `json:"datasource_url,omitempty"`
	APIKeyFilePath    string `json:"apikey_filepath,omitempty"`
	HttpAction        string `json:"http_action,omitempty"`
	BackoffInterval   int `json:"backoff_interval,omitempty"`
	MaxBackoff        int `json:"max_backoff,omitempty"`
	RetryCount        int `json:"retry_count,omitempty"`
	Timeout           int `json:"timeout,omitempty"`
}
/*
	Each dispatcher is tied to a worker and thread
*/
type DispatcherMandatoryInputs struct {
	UserID          string
	WorkerID        int
	ThreadID        int
	WorkerName      string
	WorkerGroup     string
	UnackMsgDurationSeconds int
}
/*
	Worker related configuration (self explanatory). Every worker must have a dispatcher 
*/
type Worker struct {
	Name            	    string     `json:"name" binding:"required"`
	NumberOfThreads 	    int        `json:"number_of_threads" binding:"required"`
	SampleFreqSec		    int        `json:"sample_freq_sec,omitempty"`
	SleepTimeSec		    int        `json:"sleep_time_sec,omitempty"`
	MaxRestarts			    int		 `json:"max_restarts,omitempty"`
	Dispatcher      	    Dispatcher `json:"dispatcher" binding:"required"` 
	IdleThresholdSec        int  		 `json:"idle_threshold_sec" binding:"required"`
	// Delete message which were uack for IdleThresholdSec - different for each worker depends on the dispatcher expected load
	UnackMsgDurationSeconds int 		 `json:"unack_msg_duration_seconds" binding:"required"`
}
// Prefix for request/chat metadata info basically what comes from InitChatRequest (METADATA_<chat id>)
const MetadataInternalChat = "METADATA_"
// Prefix for the next sequanc eafter  InitChatRequest or after setting a request (SetRequest) - this is part of the message in STREA_<chat id> upon successful processing so the next request can be sent (with SetRequest)
const ReqProcessingSeq     = "SEQ_"
//Messages in stream that were unacknoledged (i.e. were not picked up by a worker) for X second
const TimeoutSyncMessageSeconds = 6000000000
const WorkerStreamTemplateName = "WS_WorkerGrp-%s_WorkerInd%d"

//Default persistant time for a stream,meta and inital keys for a req id
const PersistanceTimeSeconds = 300000//60*60 // Default 48 hours

const KeyPattern = "seq_%02d_workergroup_%s_wrkind_%02d_reqid_%s"
// Meta data for the dispatchers (in this specific case it structure that will cipher/decipher persons' names)
const MetaReqID  = "meta_workergroup_%s_reqid_%s"


