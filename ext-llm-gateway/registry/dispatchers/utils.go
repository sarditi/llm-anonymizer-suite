package dispatchers

import (
    "ext-llm-gateway/models"

    "fmt"
)

func GenerateKeysList(reqID, workerGrp string, workerInd, lastN, startSeq int) []string {
    if lastN <= 0 || startSeq < 0 {
        return []string{}
    }

    end := startSeq - lastN + 1
    if end < 0 {
        end = 0
    }

    var keys []string
    for i := startSeq; i >= end; i-- {
        keys = append(keys, fmt.Sprintf(models.KeyPattern, i, workerGrp, workerInd, reqID))
    }
    return keys
}

func MergeWriteReadKeys(writeKeys []string, readKeys []string, writeAccessContentMeta string, readAccessContentMeta string) ([]string, []string){
    if writeAccessContentMeta != "" {
        writeKeys = append(writeKeys,writeAccessContentMeta)
    }
    if readAccessContentMeta != "" {
        readKeys = append(readKeys,readAccessContentMeta)
    }
	return writeKeys, readKeys
}

