package orchestrator

import (
	"ext-llm-gateway/models"
)

//Any handler used for the worker group (we curremntlu use HTTP but it can be gRPC or any other) should comply to the interface
//Be registered for instantiation in registry/registry.go and located in registry/handlers
type Handler interface {
	//Initiating the dispatcher
	InitHandler(workerGrp *models.Workergroup, Wh *Mux, cfg models.Handler)
	Start()
}
//Any Dispatchers used within the worker group  should comply to the interface
//Be registered for instantiation in registry/registry.go and located in registry/dispoatchers
type Dowork interface {
	//Initiating the dispatcher
	InitDispatcher(cfg models.Dispatcher)
	//The actual execution function
	Dowork(dispatcherInputs *models.DispatcherMandatoryInputs, reqid string,seq int,ds Datasource,synchDS SyncDSForDispatcher,args ...any) (bool, string) 
}
//Any Data source used within the worker group  should comply to the interface. We are currently using Redis DB as datasource
//Should be registered for instantiation in registry/registry.go and located in registry/datasources
type Datasource interface {
	//Initiating the Datasource
	InitDataSource(cfg models.DataSourceConfig)
	/*
		Setting up the content.
		inputs:
		reqid - the requirement id (i.e. chat id)
		data - the content
		workergrp - the relate dworker group
		workerInd - the relted worker ind 
		sequance - sequance of the chat
		persistTimeSec - how long should the content be persisted
		return false and error message if failed , true is success
	*/
	SetContent(reqid string, content *models.Content, workergrp string, workerInd int,sequance int,persistTimeSec int,args ...any) (bool, string)
	/*
		Setting up the content meta data.
		inputs:
		reqid - the requirement id (i.e. chat id)
		data - the content metadata
		workergrp - the relate dworker group
		workerInd - the relted worker ind 
		sequance - sequance of the chat
		persistTimeSec - how long should the content be persisted
		return false and error message if failed , true is success
	*/
	SetContentMeta(reqid string, data []byte, workergrp string,persistTimeSec int,args ...any) (bool, string)
	/*
		Getting last N request.
	*/
	GetLastNContents(reqid string, workergrp string, workerInd int,lastNsequances int,startingSeq int,args ...any) (bool, string)
	// Get content meta
	GetContentMeta(reqid string, workergrp string,args ...any) (bool, string)
	//Setting up temp permission to users (true in this impemetation where access to data is allowed per specific JIT user)
	SetTempAclUserPermissions(aclTempUser string, aclTempUserPassword string, readAccessKeys []string, writeAccessKeys []string, args ...any) (bool, string)
	//overdueChatIds is a list of req ids/chat ids that all related data should be removed - this method is fired by the orchestrator frequently from security and resources' reasons  
	DBMaintainenceProc(overdueChatIds []string)
}
//An inteface the synchronizer comply to and shared with the workers so they would be able to update the stream related to the adapter (STREAM_<chat id>) with the content
type SyncDSForDispatcher interface {
	SetFinalResponseInAdapterStream(reqid string, getResponseFromStream *models.GetResponseFromStream) (bool, string)
}