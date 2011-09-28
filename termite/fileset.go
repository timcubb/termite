package termite

import (
	"log"
	"os"
	"sync"
)

var _ = log.Println

type fileSetWaiter struct {
	master *Master
	mirror *mirrorConnection
	sync.Mutex
	channels map[chan int]bool
}

func newFileSetWaiter(master *Master, mc *mirrorConnection) *fileSetWaiter {
	return &fileSetWaiter{
		mirror: mc,
		master: master,
		channels: make(map[chan int]bool),
	}
}

func (me *fileSetWaiter) broadcast(id int, ignore chan int) {
	me.Lock()
	defer me.Unlock()
	for ch, _ := range me.channels {
		if ch != ignore {
			ch <- id
		}
	}
}

func (me *fileSetWaiter) getChannel() chan int {
	me.Lock()
	defer me.Unlock()

	c := make(chan int, 10)	// TODO - what is a good size?
	me.channels[c] = true
	return c
}

func (me *fileSetWaiter) flush() {
	me.Lock()
	defer me.Unlock()
	for ch, _ := range me.channels {
		close(ch)
	}
}

func (me *fileSetWaiter) wait(rep *WorkResponse, completion chan int) (err os.Error) {
	id := rep.FileSetId
	if rep.FileSet != nil {
		err = me.master.replayFileModifications(me.mirror.rpcClient, rep.FileSet.Files)
		if err == nil {
			me.master.fileServer.updateFiles(rep.FileSet.Files)
			me.master.mirrors.queueFiles(me.mirror, rep.FileSet.Files)
			me.broadcast(id, completion)
		} else {
			me.flush()
		}
	} else {
		for {
			c, ok := <-completion
			if !ok {
				return os.NewError("files were never sent.")
			}
			if c == id {
				break
			}
		}
	}
	
	me.Lock()
	defer me.Unlock()
	me.channels[completion] = false, false
	return err
}
