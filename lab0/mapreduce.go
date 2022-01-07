package main

import (
	"bufio"
	"encoding/json"
	"hash/fnv"
	"io/ioutil"
	"log"
	"os"
	"path"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// KeyValue is a type used to hold the key/value pairs passed to the map and reduce functions.
type KeyValue struct {
	Key   string
	Value string
}

// ReduceF function from MIT 6.824 LAB1
type ReduceF func(key string, values []string) string

// MapF function from MIT 6.824 LAB1
type MapF func(filename string, contents string) []KeyValue

// jobPhase indicates whether a task is scheduled as a map or reduce task.
type jobPhase string

const (
	mapPhase    jobPhase = "mapPhase"
	reducePhase          = "reducePhase"
)

type task struct {
	dataDir    string
	jobName    string
	mapFile    string   // only for map, the input file
	phase      jobPhase // are we in mapPhase or reducePhase?
	taskNumber int      // this task's index in the current phase
	nMap       int      // number of map tasks
	nReduce    int      // number of reduce tasks
	mapF       MapF     // map function used in this job
	reduceF    ReduceF  // reduce function used in this job
	wg         sync.WaitGroup
}

// MRCluster represents a map-reduce cluster.
type MRCluster struct {
	nWorkers int
	wg       sync.WaitGroup
	taskCh   chan *task
	exit     chan struct{}
}

var singleton = &MRCluster{
	nWorkers: runtime.NumCPU(),
	taskCh:   make(chan *task),
	exit:     make(chan struct{}),
}

func init() {
	singleton.Start()
}

// GetMRCluster returns a reference to a MRCluster.
func GetMRCluster() *MRCluster {
	return singleton
}

// NWorkers returns how many workers there are in this cluster.
func (c *MRCluster) NWorkers() int { return c.nWorkers }

// Start starts this cluster.
func (c *MRCluster) Start() {
	for i := 0; i < c.nWorkers; i++ {
		c.wg.Add(1)
		go c.worker()
	}
}

func (c *MRCluster) worker() {
	defer c.wg.Done()
	for {
		select {
		case t := <-c.taskCh:
			if t.phase == mapPhase {
				content, err := ioutil.ReadFile(t.mapFile)
				if err != nil {
					panic(err)
				}

				fs := make([]*os.File, t.nReduce)
				bs := make([]*bufio.Writer, t.nReduce)
				for i := range fs {
					rpath := reduceName(t.dataDir, t.jobName, t.taskNumber, i)
					fs[i], bs[i] = CreateFileAndBuf(rpath)
				}
				results := t.mapF(t.mapFile, string(content))
				for _, kv := range results {
					enc := json.NewEncoder(bs[ihash(kv.Key)%t.nReduce])
					if err := enc.Encode(&kv); err != nil {
						log.Fatalln(err)
					}
				}
				for i := range fs {
					SafeClose(fs[i], bs[i])
				}
			} else {
				// YOUR CODE HERE :)
				// hint: don't encode results returned by ReduceF, and just output
				// them into the destination file directly so that users can get
				// results formatted as what they want.
				inter_results := map[string][]string{}
				results := []string{}
				for i := 0; i < t.nMap; i++ {
					rpath := reduceName(t.dataDir, t.jobName, i, t.taskNumber)
					content, err := ioutil.ReadFile(rpath)
					if err != nil {
						log.Printf("[INFO] Not found reduce file %v-%v", i, t.taskNumber)
						panic(err)
					}
					if len(content) == 0 {
						continue
					}
					lines := strings.Split(string(content), "\n")

					// TODO
					var kvs KeyValue
					for j := 0; j < len(lines); j++ {
						if len(lines[j]) != 0 {
							err = json.NewDecoder(strings.NewReader(lines[j])).Decode(&kvs)
							inter_results[kvs.Key] = append(inter_results[kvs.Key], kvs.Value)
						}
					}
					// fmt.Printf("[INFO] inter result, i: %v, value: %v\n", i, len(inter_results[kvs.Key]))
				}
				opath := mergeName(t.dataDir, t.jobName, t.taskNumber)
				fs, bs := CreateFileAndBuf(opath)
				for k, v := range inter_results {
					results = append(results, t.reduceF(k, v))
				}

				for _, v := range results {
					// fmt.Printf("[INFO] result: %s\n", v)
					bs.WriteString(v)
				}
				SafeClose(fs, bs)
				// panic("YOUR CODE HERE")
			}
			t.wg.Done()
		case <-c.exit:
			return
		}
	}
}

// Shutdown shutdowns this cluster.
func (c *MRCluster) Shutdown() {
	close(c.exit)
	c.wg.Wait()
}

// Submit submits a job to this cluster.
func (c *MRCluster) Submit(jobName, dataDir string, mapF MapF, reduceF ReduceF, mapFiles []string, nReduce int) <-chan []string {
	notify := make(chan []string)
	go c.run(jobName, dataDir, mapF, reduceF, mapFiles, nReduce, notify)
	return notify
}

func (c *MRCluster) run(jobName, dataDir string, mapF MapF, reduceF ReduceF, mapFiles []string, nReduce int, notify chan<- []string) {
	// map phase
	nMap := len(mapFiles)
	tasks := make([]*task, 0, nMap)
	for i := 0; i < nMap; i++ {
		t := &task{
			dataDir:    dataDir,
			jobName:    jobName,
			mapFile:    mapFiles[i],
			phase:      mapPhase,
			taskNumber: i,
			nReduce:    nReduce,
			nMap:       nMap,
			mapF:       mapF,
		}
		t.wg.Add(1)
		tasks = append(tasks, t)
		go func() { c.taskCh <- t }()
	}
	for _, t := range tasks {
		t.wg.Wait()
	}

	// reduce phase
	// YOUR CODE HERE :D
	// 1. run reduce function
	reduce_task := make([]*task, 0, nReduce)
	for i := 0; i < nReduce; i++ {
		t := &task{
			dataDir:    dataDir,
			jobName:    jobName,
			phase:      reducePhase,
			taskNumber: i,
			nReduce:    nReduce,
			nMap:       nMap,
			reduceF:    reduceF,
		}
		t.wg.Add(1)
		reduce_task = append(reduce_task, t)
		go func() { c.taskCh <- t }()
	}
	for _, t := range reduce_task {
		t.wg.Wait()
	}
	// 2. collect result and generate the final result
	// med_results := map[string]int{}
	result := []string{}
	for i := 0; i < nReduce; i++ {
		opath := mergeName(dataDir, jobName, i)
		result = append(result, opath)
		// content, err := ioutil.ReadFile(opath)
		// if err != nil {
		// 	log.Printf("[INFO] Not found reduce output file %v\n", i)
		// 	panic(err)
		// }
		// // fmt.Printf("[INFO] len of content, %v\n", len(content))
		// if len(content) == 0 {
		// 	continue
		// }
		// lines := strings.Split(string(content), "\n")
		// // log.Println("[INFO] line 0 in map task, ", lines[0])
		// // TODO
		// for j := 0; j < len(lines); j++ {
		// 	if len(lines[j]) != 0 {
		// 		str_kv := strings.Split(lines[j], " ")
		// 		tmp, err := strconv.Atoi(str_kv[1])
		// 		if err != nil {
		// 			panic(err)
		// 		}
		// 		med_results[str_kv[0]] += tmp
		// 	}
		// }
	}

	// // 3. put into the notify chan
	// for k, v := range med_results {
	// 	result = append(result, k+" "+strconv.Itoa(v))
	// }
	// // fmt.Printf("[INFO] final result %s\n", result[0])
	notify <- result
	// // panic("YOUR CODE HERE")
}

func ihash(s string) int {
	h := fnv.New32a()
	h.Write([]byte(s))
	return int(h.Sum32() & 0x7fffffff)
}

func reduceName(dataDir, jobName string, mapTask int, reduceTask int) string {
	return path.Join(dataDir, "mrtmp."+jobName+"-"+strconv.Itoa(mapTask)+"-"+strconv.Itoa(reduceTask))
}

func mergeName(dataDir, jobName string, reduceTask int) string {
	return path.Join(dataDir, "mrtmp."+jobName+"-res-"+strconv.Itoa(reduceTask))
}
