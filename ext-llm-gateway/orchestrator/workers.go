package orchestrator

import (
	"ext-llm-gateway/models"
	"ext-llm-gateway/logger"

	"context"
	"fmt"
	"time"
	"strconv"
)
// Worker is the framework/thread which dispatches Dispatcher (by executing Dowork)
// Each worker has a stream it listens, pulls messages and activate the Dowork in the dispatchers doworkAndACKPublish
// Upon success it will notify the synchronizer to ack the message and publish to the next worked stream AckAndPublishToNextStreamAtomic
// Once in a while it will check for lingering unack messages (probably from failed processes in other pods of the gateway) and process them ClaimNAckFromStream
// It will frequently sample NACK messages that were in the stream for too long and delete them DelOldNAckFromStream
type Worker struct {
	dMI			             *models.DispatcherMandatoryInputs
	maxRestarts               int
	dW                        Dowork
	sleepTimeSec              time.Duration
	currStream                string
	nextStreamInSeq           string
	Syn 					  *SynchRedisDatasource
	synDis                    SyncDSForDispatcher
	IdleThresholdSec          int
	DS 						  Datasource
}

func NewWorker(mw models.Worker, workflowseq int, workergroup string,threadSeq int,dW Dowork, workerStreamTemplateName string,Syn *SynchRedisDatasource,ds Datasource,synDis SyncDSForDispatcher) *Worker {
	logger.Log.Info(fmt.Sprintf("Initiating  workername: %s seq=%d thread=%d in worker group: %s", mw.Name, workflowseq, threadSeq, workergroup))
	var nextStreamInSeq string
	if mw.Name == "_FINALIZER_" {
		nextStreamInSeq = "NA"
	} else {
		nextStreamInSeq = fmt.Sprintf(workerStreamTemplateName,workergroup,workflowseq+1)
	}

	w := &Worker{sleepTimeSec: 1*time.Second,maxRestarts: 3,
		dW: dW, 
		dMI: &models.DispatcherMandatoryInputs{
				UserID: fmt.Sprintf(models.ProcessID + workerStreamTemplateName + "_threadid-%d",workergroup,mw.Name,workflowseq,threadSeq),
				ThreadID: threadSeq,
				WorkerID: workflowseq,
				WorkerName: mw.Name,
				WorkerGroup: workergroup,
				UnackMsgDurationSeconds: mw.UnackMsgDurationSeconds,
			},
		currStream: fmt.Sprintf(workerStreamTemplateName,workergroup,workflowseq),
		nextStreamInSeq: nextStreamInSeq,
		Syn:Syn,
		IdleThresholdSec: mw.IdleThresholdSec,
		DS: ds,
		synDis: synDis,
	}

	if mw.SleepTimeSec > 0  {w.sleepTimeSec = time.Duration(mw.SleepTimeSec)*time.Second}
	if mw.MaxRestarts > 0 {w.maxRestarts = mw.MaxRestarts}
	return w
}

func (w *Worker) doworkAndACKPublish(msgID string, msg string) {
		// ---- Worker Logic ----
	seq, _ := strconv.Atoi(msg[:2])
	reqid := msg[3:]
	var okDowork bool
	var err string
	if okDowork,err = w.dW.Dowork(w.dMI,reqid, seq, w.DS, w.synDis); !okDowork {
		logger.Log.WithWorker(w.dMI).Error(fmt.Sprintf("dowork failed for msgid %s reqid %s - %s. Message will NOT be acknowledged", msgID, reqid, err))	
	}
	// A basic function in the process hence the panic
	if okDowork {	
		if ok := w.Syn.AckAndPublishToNextStreamAtomic(w.currStream, w.nextStreamInSeq, msgID, msg); !ok {
			panic(logger.Log.WithWorker(w.dMI).Fatal(fmt.Sprintf("In process id %s, unable to AckAndPublishToNextStreamAtomic with currStream %s and reqid %s", w.dMI.UserID, w.currStream, msg)))
		}
	}
}

func (w *Worker) Start(ctx context.Context) {
	go func() {
		restartCount := 0

		for {
			select {
			case <-ctx.Done():
				logger.Log.WithWorker(w.dMI).Debug("Worker stopped")
				return
			default:
			}

			func() {
				defer func() {
					if r := recover(); r != nil {
						restartCount++
						logger.Log.WithWorker(w.dMI).Debug(fmt.Sprintf("Worker crashed (%d/%d): %v", restartCount, w.maxRestarts, r))
						// Exceeded retry limit → panic entire service
						if restartCount >= w.maxRestarts {
							panic(logger.Log.WithWorker(w.dMI).Fatal(fmt.Sprintf("Worker failed too many times (%d)", 2/*restartCount*/)))
						}
					}
				}()
				//Fetching messages from worker stream if new ones exist
				if ok, msgID, msg := w.Syn.FetchMessageFromStream(w.currStream, w.dMI.UserID); ok && msgID != "NA" {
					if msgID != "NA" {
						logger.Log.WithWorker(w.dMI).Debug(fmt.Sprintf("Fetched msgid FromStream %s", msgID))
					}
					w.doworkAndACKPublish(msgID, msg)
				}
				//Claiming unack messages from worker stream (messages that are passed the ack time probably because of other workewrs crash)
				if ok, msgID, msg := w.Syn.ClaimNAckFromStream(w.currStream, w.dMI.UserID, time.Duration(w.IdleThresholdSec) * time.Second); ok && msgID != "NA" {
					if msgID != "NA" {
						logger.Log.WithWorker(w.dMI).Debug(fmt.Sprintf("Fetched msgid NAckFromStream %s", msgID))
					}
					w.doworkAndACKPublish(msgID, msg)
				}
				//Deleting Old unack messages and notifying adapter's stream
				if ok, msg := w.Syn.DelOldNAckFromStream(w.currStream, w.dMI.UserID, w.dMI.UnackMsgDurationSeconds); !ok  {
					logger.Log.WithWorker(w.dMI).Error(fmt.Sprintf("Failing to delete old UNACK messages from stream %s, consumer %s with error %s", w.currStream, w.dMI.UserID, msg))
				}

				// Reset restart counter on successful run
				restartCount = 0
			}()
			time.Sleep(w.sleepTimeSec)
		}
	}()
}

