package dispatchers

import (
    "ext-llm-gateway/models"
	"ext-llm-gateway/logger"
	"ext-llm-gateway/orchestrator"

    "time"
    "fmt"
    "encoding/json"
    "bytes"
    "io"
    "net/http"

   "github.com/gojek/heimdall/v7"
   "github.com/gojek/heimdall/v7/httpclient"
   "github.com/google/uuid"
)
// See orchestrator/interface.go for the Dowrok interface (Dispatcher). This is an implementation of the interface for REDISACL_RESTAPI as mentioned in registry/registry.go
// The Dispatcher sent to cihere url, gemini url or deciphere url set of allowed keys and temporary useers to access, create new processed data and create new metadat for the request
// in the case ciphering is needed.
// we have 3 different dispatchers all of the kind of REDISACL_RESTAPI and differ between one anothe rin the dispatcher url (cipherer, gemioni, decipher) the data is sent to 
// And the allowed keys to read and update for each dispatcher. The decision was to put it in one logical REDISACL_RESTAPI and handle the internal logic that is different in (cipherer, gemioni, decipher)
// in one place 
type RestAPIDispatcher struct{
	backoffInterval time.Duration
	maxBackoff      time.Duration
	jitter          time.Duration
	timeout         time.Duration
	retryCount      int
    llmRepWorkerIND  int
	url             string
	httpAction      string
    redisURL        string    
    client          *httpclient.Client
}

type Option func(*RestAPIDispatcher)
const httpClientTimeoutInSec = 20
// Functional options
func WithBackoffInterval(d int) Option {
	return func(r *RestAPIDispatcher) { if d > 0 { r.backoffInterval = time.Duration(d)*time.Second } }
}
func WithMaxBackoff(d int) Option {
	return func(r *RestAPIDispatcher) { if d > 0 { r.maxBackoff = time.Duration(d)*time.Second }}
}
func WithRetryCount(n int) Option {
	return func(r *RestAPIDispatcher) { if n != 0 { r.retryCount = n }}
}
func WithTimeout(n int) Option {
	return func(r *RestAPIDispatcher) { if n > 0 { r.timeout = time.Duration(n)*time.Second }}
}

func (d *RestAPIDispatcher)InitDispatcher(cfg models.Dispatcher)  {
    d.url = cfg.URL
    d.httpAction = cfg.HttpAction
    d.redisURL = cfg.DatasourceUrl
    d.backoffInterval = time.Duration(cfg.BackoffInterval)*time.Second 
    d.maxBackoff = time.Duration(cfg.MaxBackoff)*time.Second 
    d.timeout = time.Duration(cfg.Timeout)*time.Second 
    d.retryCount = cfg.RetryCount

    backoff := heimdall.NewExponentialBackoff(d.backoffInterval, d.maxBackoff, 2.0, 500*time.Millisecond)

    // Create a retry mechanism with retries
    retrier := heimdall.NewRetrier(backoff)

    // HTTP client
    d.client = httpclient.NewClient(
        httpclient.WithHTTPTimeout(d.timeout), // per request timeout
        httpclient.WithRetrier(retrier),
        httpclient.WithRetryCount(d.retryCount),
    )
}


func NewRestAPIDispatcher(url string,httpAction string, redisURL string, opts ...Option) *RestAPIDispatcher {
    
    logger.Log.Info("Starting NewRest API Dispatcher..")
    r := &RestAPIDispatcher{
		backoffInterval:  2*time.Second,
		maxBackoff:  30*time.Second,
		jitter:  500*time.Millisecond,
		timeout:  httpClientTimeoutInSec*time.Second,
		retryCount: 3,
		url: url,
        httpAction: httpAction,
        redisURL: redisURL,  
        llmRepWorkerIND: -1,      
	}

    for _, o := range opts {o(r)}

    backoff := heimdall.NewExponentialBackoff(r.backoffInterval, r.maxBackoff, 2.0, r.jitter)

    // Create a retry mechanism with retries
    retrier := heimdall.NewRetrier(backoff)

    // HTTP client
    r.client = httpclient.NewClient(
        httpclient.WithHTTPTimeout(r.timeout), // per request timeout
        httpclient.WithRetrier(retrier),
        httpclient.WithRetryCount(r.retryCount),
    )
	return r
}

func (d *RestAPIDispatcher)Dowork(di *models.DispatcherMandatoryInputs, reqid string,seq int,ds orchestrator.Datasource,synchDS orchestrator.SyncDSForDispatcher, args ...any) (bool, string) {

    logger.Log.WithWorker(di).Debug(fmt.Sprintf("Processing reqid - %s", reqid))

    reqRedisAccessPermissions := &models.ReqRedisAccessPermissions{
        ReadAccessKeys:  []string{},
        WriteAccessKeys: []string{},
        ReadAccessContentMeta: 	"",
	    WriteAccessContentMeta:	"",
        RedisURL: d.redisURL,
    }

    reqRedisAccessPermissions.UserID = fmt.Sprintf("%s_%d_ACL_%s",di.WorkerGroup, di.WorkerID,reqid)
    reqRedisAccessPermissions.Pwd = uuid.New().String()

    if di.WorkerName == "cipher_req" {
        // Send to cipher request and responses from the 2nd request
        reqRedisAccessPermissions.ReadAccessKeys = GenerateKeysList(reqid, di.WorkerGroup, di.WorkerID, 1, seq)
        reqRedisAccessPermissions.ReadAccessContentMeta = fmt.Sprintf(models.MetaReqID,di.WorkerGroup,reqid)
        reqRedisAccessPermissions.WriteAccessContentMeta = fmt.Sprintf(models.MetaReqID,di.WorkerGroup,reqid) 
        reqRedisAccessPermissions.WriteAccessKeys = append(reqRedisAccessPermissions.WriteAccessKeys,fmt.Sprintf(models.KeyPattern,seq,di.WorkerGroup,di.WorkerID+1,reqid),)
    }

    if di.WorkerName == "decipher_resp" {
        reqRedisAccessPermissions.ReadAccessKeys = GenerateKeysList(reqid, di.WorkerGroup,di.WorkerID, 1, seq)
        reqRedisAccessPermissions.ReadAccessContentMeta = fmt.Sprintf(models.MetaReqID,di.WorkerGroup,reqid)
        reqRedisAccessPermissions.WriteAccessKeys = append(reqRedisAccessPermissions.WriteAccessKeys, fmt.Sprintf(models.KeyPattern,seq,di.WorkerGroup,di.WorkerID+1,reqid),)
    }

    if di.WorkerName == "send_to_gemini" {
        if seq > 0 {
            reqRedisAccessPermissions.ReadAccessKeys = d.interleave(GenerateKeysList(reqid, di.WorkerGroup, di.WorkerID, 1000, seq),GenerateKeysList(reqid, di.WorkerGroup, d.llmRepWorkerIND, 1000, seq - 1))
        } else {
            reqRedisAccessPermissions.ReadAccessKeys = GenerateKeysList(reqid, di.WorkerGroup, di.WorkerID, 1000, seq)
        }
        reqRedisAccessPermissions.WriteAccessKeys = append(reqRedisAccessPermissions.WriteAccessKeys,fmt.Sprintf(models.KeyPattern,seq,di.WorkerGroup,di.WorkerID+1,reqid),)
    }

    // For debugging purposes only #####################################################################
    data, _ := json.Marshal(reqRedisAccessPermissions)
    logger.Log.WithWorker(di).Debug(fmt.Sprintf("ACL for reqid %s - %s", reqid, string(data)))
    // For debugging purposes only #####################################################################
    writeKeys,readKeys := MergeWriteReadKeys(reqRedisAccessPermissions.WriteAccessKeys, reqRedisAccessPermissions.ReadAccessKeys, reqRedisAccessPermissions.WriteAccessContentMeta, reqRedisAccessPermissions.ReadAccessContentMeta)

    if ok, errMsg := ds.SetTempAclUserPermissions(reqRedisAccessPermissions.UserID, reqRedisAccessPermissions.Pwd,readKeys,writeKeys); !ok {
        return ok, errMsg
    }
    // This together with a kube definition will alwys direct the request to the same pod
    // # ==============================================================================
    // # ISTIO ROUTING SUMMARY: Header-based routing via X-User-ID
    // # ==============================================================================
    // # 1. Label specific Pods in your Deployment (e.g., version: targeted).
    // # 2. Create a DestinationRule to group those labeled Pods into a 'subset'.
    // # 3. Create a VirtualService with a 'match' rule for the 'X-User-ID' header.
    // # 4. Point the matching traffic to the 'subset' defined in the DestinationRule.
    // #
    // # NOTE: Ensure the Istio sidecar is injected in the namespace, otherwise
    // # the VirtualService rules will be ignored.
    // #
    // # OFFICIAL DOCUMENTATION & STEP-BY-STEP:
    // # https://istio.io/latest/docs/tasks/traffic-management/request-routing/#route-based-on-user-identity
    // # ==============================================================================
    headers := http.Header{}
    headers.Set("Content-Type", "application/json")
    headers.Set("X-User-ID", reqid)
    
	logger.Log.WithWorker(di).Debug(fmt.Sprintf("Posting for ReqID %s, with URL %s", reqid,d.url))
    resp, err := d.client.Post(d.url, bytes.NewBuffer(data), headers)
    if err != nil {
        return false, err.Error()
    }

    defer resp.Body.Close()
    bodyBytes, err := io.ReadAll(resp.Body)

    if err != nil {
        return false, "failed to read response body: " + err.Error()
    }
    bodyString := string(bodyBytes)

    if resp.StatusCode != 200 {
        // Return both the status AND the body (often contains the error message from the API)
        return false, fmt.Sprintf("HTTP %d: %s", resp.StatusCode, bodyString)
    }
    // Set the worker ind to a valid value if the response from the llm was processed successful 
    if di.WorkerName == "send_to_gemini" && d.llmRepWorkerIND == -1 {
        d.llmRepWorkerIND = di.WorkerID+1 //That would be the index ll will write its reply to
    }

	return true, "r"
}

func (d *RestAPIDispatcher)interleave(userReq []string, modelResp []string) []string {
    var result []string
    
    // Determine the length of the longer slice to cover all elements
    maxLen := len(userReq)
    if len(modelResp) > maxLen {
        maxLen = len(modelResp)
    }

    for i := 0; i < maxLen; i++ {
        // Append from userReq if index exists
        if i < len(userReq) {
            result = append(result, userReq[i])
        }
        // Append from modelResp if index exists
        if i < len(modelResp) {
            result = append(result, modelResp[i])
        }
    }

    return result
}

