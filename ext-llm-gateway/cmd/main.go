package main

import (
	"ext-llm-gateway/registry"

	"ext-llm-gateway/orchestrator"
	"ext-llm-gateway/models"
	"ext-llm-gateway/logger"

	"fmt"
	"context"
	"strconv"
	"os"
	"encoding/json"

	"github.com/google/uuid"

)


func main() {
	
	//Read configuration file
	var LlmExtGatewayConfig models.LlmExtGwConfig
	extLlmConf := func() string {
		if v := os.Getenv("EXT_LLM_GW_CONFIG"); v != "" {
			return v
		}
		return "./conf/config.json"
	}()

	val, err := os.ReadFile(extLlmConf)
	if err != nil {
		panic(err)
	}

	if err := json.Unmarshal([]byte(val), &LlmExtGatewayConfig); err != nil {
		panic(err)
	}

	models.ProcessID = uuid.New().String()

	v := LlmExtGatewayConfig.WorkerGroup 

	//Load Redis synch Data source from registry per configuration 
	synchDS := &orchestrator.SynchRedisDatasource{}
	synchDS.InitDataSource(LlmExtGatewayConfig.SynchDB) 

	//Load Data source from registry per configuration 
	dataS := registry.CreateInstance(LlmExtGatewayConfig.Datasource.Name).(orchestrator.Datasource)
	dataS.InitDataSource(LlmExtGatewayConfig.Datasource) 

	//Initiate orchestrator
	wh := orchestrator.NewMux(dataS, synchDS, v.Prompt, v.WorkerGroupName, v.SessionTTLSec)
	
	// Load and start handler from registry per configuration
	hndlr := registry.CreateInstance(LlmExtGatewayConfig.Handler.Name).(orchestrator.Handler)
	hndlr.InitHandler(&v,wh,LlmExtGatewayConfig.Handler)
	hndlr.Start()
	
	// Set max allowed message size threshold
	v.ValRedisMaxSizeByte = func(s string) int {
	n, err := strconv.Atoi(s)
		if err != nil {
			panic(err)
		}
		return n*1024
	}(v.ValRedisMaxSizeInMB)


	// Assemble the workers per worker group configuration
	ctx, _ := context.WithCancel(context.Background())
	wh.SetFirstWorkerStream(v.WorkerGroupName,v.Workers[0].Name,0)
	for iw, vw := range v.Workers {
		logger.Log.Info(fmt.Sprintf("Loading DispatcherProtocol=%s Workename=%s for WorkerGroupName=%s",vw.Dispatcher.Protocol,vw.Name,v.WorkerGroupName))
		//Load a dispatcher from registry per configuration 
		dw := registry.CreateInstance(vw.Dispatcher.Protocol).(orchestrator.Dowork)
		dw.InitDispatcher(vw.Dispatcher)
		//Create worker for a dispatcher and required working stream
		synchDS.CreateWorkerStream(fmt.Sprintf(models.WorkerStreamTemplateName,v.WorkerGroupName,iw))
		for i := 0; i < vw.NumberOfThreads; i++ {
			worker := orchestrator.NewWorker(vw, iw, v.WorkerGroupName,i,dw,models.WorkerStreamTemplateName,synchDS, dataS,synchDS)
			worker.Start(ctx)
		}
	}
	
    select {} 
}
