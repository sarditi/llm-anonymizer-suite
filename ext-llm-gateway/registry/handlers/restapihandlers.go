package handlers

import (
	"ext-llm-gateway/orchestrator"
	"ext-llm-gateway/models"
	"ext-llm-gateway/logger"
	"ext-llm-gateway/utils"
	"fmt"

	"github.com/gin-gonic/gin"
)
// See orchestrator/interface.go for the Handler interface. This is an implementation of the interface for Gin HTTP/RESTAPI
// The instance is being instantiated "reflection-like" with registry/registry.go with the key GINRESTAPI
type RestAPIHandler struct {
	WorkerGrp *models.Workergroup
	Wh *orchestrator.Mux
	port string
	ginInstance *gin.Engine
	adapters   []string  
}

const timeoutSynchReq = 20 * 1000

func (h *RestAPIHandler) InitHandler(workerGrp *models.Workergroup, Wh *orchestrator.Mux, cfg models.Handler)  {
	h.WorkerGrp = workerGrp
	h.Wh = Wh
	h.port = cfg.Port
	h.adapters = cfg.Adapters
	h.ginInstance = gin.Default()

	logger.Log.Info("Starting Gin RestAPIHandler...")
	h.ginInstance.POST("/init_chat", h.InitChat)
	h.ginInstance.POST("/set_req_from_adapter", h.SetReqFromAdapter)
	h.ginInstance.POST("/set_req_from_adapter_sync", h.SetReqFromAdapterSync)
}

func (h *RestAPIHandler) Start(){
	go func() {
		if err := h.ginInstance.Run(":" + h.port); err != nil {
			logger.Log.Fatal(fmt.Sprintf("Failed to start server: %s", err.Error))
		}
	}()
}

func (h *RestAPIHandler) SetReqFromAdapterSync(c *gin.Context) {
	var content *models.Content
	data, _ := c.GetRawData()
	code, _, internalChatID, retMsg := SetReqToStream(data, h.WorkerGrp.ValRedisMaxSizeByte, h.Wh.Prompt, h.Wh)
	if code != 0 {
		c.JSON(utils.GRPCToHTTPStatus(code), models.SetReqHttpResponse{
			Status:  "error",
			ChatID: internalChatID,
			Message: retMsg,
		})
		return	
	}

	code, internalChatID, retMsg, content = GetContentFromStream(internalChatID, h.Wh.Syn)

	if code != 0 {
		logger.Log.Error("SetReqFromAdapterSync failed for session id : " + internalChatID + " - " + retMsg)
		c.JSON(utils.GRPCToHTTPStatus(code), models.SetReqHttpResponse{
			Status:  "error",
			ChatID: internalChatID,
			Message: retMsg,
		})
		return		
	}

	c.JSON(utils.GRPCToHTTPStatus(code), models.SetReqHttpSyncResponse{
		Status:  		"success",
		ChatID: 		internalChatID,
		SetNextReqKey:	retMsg,  
		Content: 		*content,
	})		
}

func (h *RestAPIHandler) SetReqFromAdapter(c *gin.Context) {

	data, _ := c.GetRawData()
	
	// Validate input
	code, _, internalChatID, retMsg := SetReqToStream(data, h.WorkerGrp.ValRedisMaxSizeByte, h.Wh.Prompt, h.Wh)
	errMsg := "success"

	//The operator returns gRPC codes, might need mapping to http codes
	if code != 0 {
		errMsg = "error"
	}
	c.JSON(utils.GRPCToHTTPStatus(code), models.SetReqHttpResponse{
		Status:  errMsg,
		ChatID: internalChatID,
		Message: retMsg,
	})
}

func (h *RestAPIHandler) InitChat(c *gin.Context) {

	logger.Log.Debug("In InitChat Handler....")

	data, _ := c.GetRawData()
	code, internalChatID, retMsg, setReqKey, pwd := h.Wh.InitReq(data,h.adapters)
	errMsg := "success"

	//The operator returns gRPC codes, might need mapping to http codes
	if code != 0 {
		errMsg = "error"
	}
	c.JSON(utils.GRPCToHTTPStatus(code), models.InitChatResponse{
		Status:  		errMsg,
		ChatID: 		internalChatID,
		SetNextReqKey:	setReqKey,  
		Message: 		retMsg,
		StreamPwd:      pwd,
	})
}


