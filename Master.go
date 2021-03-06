/*This is the code for the network master. It will be started from an elevator if it is not already started.
The master receives events from its slave elevators and distributes the assosiated jobs*/

package main

import (
	. "./datatypes"
	. "./encoder"
	. "./network"
	"fmt"
	. "math"
	"net"
	"strconv"
	"strings"
	"time"
)

/*
-----------------------------
---------- Globals ----------
-----------------------------
*/

//Elevator map to keep track of all connected elevators
var emap = make(ElevatorMap)

//mutex for the elevator map
var emapGuard = make(chan bool, 1)

//backup of the elevator map (received from the slaves in case the master dies)
var backup = make(ElevatorMap)
var receivedBackup = bool(false)

/*
-----------------------------
--------- Functions ---------
-----------------------------
*/

//handles messages from a slave (received from the network module)
func handleMessages(msgChan chan Message) {

	for {
		m := <-msgChan
		//first bit is an id telling us what type of message it is
		msgType, _ := strconv.Atoi(string(m.Msg[0]))
		switch msgType {
		case HANDSHAKE:
			//Nothing to handle.
			break
		case EVENT:
			e := DecodeEvent(m.Msg[1:])
			fmt.Println("Event at", m.Sender.RemoteAddr())
			fmt.Println("Type:", e.EventType, "Floor:", e.Floor)

			if e.EventType == BUTTON_CALL_UP || e.EventType == BUTTON_CALL_DOWN {
				//Find most suitable elevator to handle the event
				mostSuitable := findMostSuitable(e, emap)
				if mostSuitable != nil {
					fmt.Println("Sending order to", mostSuitable.RemoteAddr())
					SendMessage(mostSuitable, e)
					//also tell all elevators to turn on the button light
					for _, elevator := range emap {
						if e.EventType == BUTTON_CALL_UP {
							SendMessage(elevator.Conn, Event{TURN_ON_UP_LIGHT, e.Floor})
						} else if e.EventType == BUTTON_CALL_DOWN {
							SendMessage(elevator.Conn, Event{TURN_ON_DOWN_LIGHT, e.Floor})
						}
					}
				} else {
					fmt.Println("SEND ORDER FAIL")
				}
			}
			if e.EventType == JOB_DONE {
				//tell all elevators to turn off lights
				for _, elevator := range emap {
					SendMessage(elevator.Conn, Event{TURN_OFF_LIGHTS, e.Floor})
				}
			}
			break
		case ELEV_INFO:
			//updated information about a single elevator
			<-emapGuard
			estruct := DecodeElevatorStruct(m.Msg[1:])
			estruct.Conn = m.Sender
			addr := m.Sender.RemoteAddr().String()
			addr = addr[0:strings.Index(addr, ":")]
			emap[addr] = estruct
			fmt.Println(addr, ": ", emap[addr])

			//send a backup to all slaves
			for _, elev := range emap {
				SendMessage(elev.Conn, emap)
			}
			emapGuard <- true
			break
		case BACKUP:
			//Bakcup from the previous master
			if !receivedBackup {
				backup = DecodeElevatorMap(m.Msg[1:])
				receivedBackup = true
			}
			break
		}
	}
}

//Find the most suitable elevator for a event
func findMostSuitable(buttonEvent Event, emapCopy ElevatorMap) *net.TCPConn {

	EventFloor := buttonEvent.Floor
	dir := 0
	if buttonEvent.EventType == BUTTON_CALL_UP {
		dir = UP
	} else if buttonEvent.EventType == BUTTON_CALL_DOWN {
		dir = DOWN
	} else {
		return nil
	}

	bestDist := 99999
	var bestElev *net.TCPConn = nil
	tempDist := 0
	maxFloor := -1

	//examine every elevator in the elevator map too see which is the best
	for _, elevator := range emapCopy {
		if (elevator.Uprun[EventFloor] > 0 && dir == UP) || (elevator.Downrun[EventFloor] > 0 && dir == DOWN) {
			//ignore if an elevator already is heading to that floor
			return nil
		}
		if (dir == elevator.Dir && dir*elevator.Current_floor < dir*EventFloor) || elevator.Dir == STOP {
			//Returns the distance between current floor and eventfloor if the elevator is going towards the eventfloor
			tempDist = int(Abs(float64(elevator.Current_floor - EventFloor)))
			if tempDist < bestDist || (tempDist == bestDist && elevator.Dir == STOP) {
				//choose this as best elevator if the elevator is in the least distance to the eventfloor, and prioritizes inactive elevators
				bestDist = tempDist
				bestElev = elevator.Conn
			}
		} else { //If the elevator is going the wrong way, calculate the distance
			if elevator.Dir == UP {
				for j := N_FLOORS - 1; j >= 0; j-- {
					if elevator.Uprun[j] > 0 {
						maxFloor = j
						break
					}
				}
			} else if elevator.Dir == DOWN {
				for j := 0; j < N_FLOORS; j++ {
					if elevator.Downrun[j] > 0 {
						maxFloor = j
						break
					}
				}
			}
			tempDist = int(Abs(float64(maxFloor-elevator.Current_floor)) + Abs(float64(maxFloor-EventFloor)))
			if tempDist < bestDist {
				bestDist = tempDist
				bestElev = elevator.Conn
			}
		}
	}
	return bestElev
}

//Distribute the jobs of a lost elevator
func DistributeJobs(elev ElevatorStruct) {
	for i := 0; i < N_FLOORS; i++ {
		if elev.Uprun[i] == CALL {
			//only distribute CALL jobs
			DistributedJob := Event{BUTTON_CALL_UP, i}
			SendMessage(findMostSuitable(DistributedJob, emap), DistributedJob)
		}
		if elev.Downrun[i] == CALL {
			DistributedJob := Event{BUTTON_CALL_DOWN, i}
			SendMessage(findMostSuitable(DistributedJob, emap), DistributedJob)
		}
	}
}

//Send new queue numbers to the elevators
func RemakeQueue() {
	i := 1
	for _, elev := range emap {
		SendMessage(elev.Conn, i)
		i += 1
	}
}

//In case this master process has been created because the previous master died, the slaves will send
//a backup of the previous masters elevator map. Wait 2 seconds in order to give all the slaves a chance to
//connect, then check the elevators currently connected against the backup. If not all elevators are present,
//we have lost one of them, and have to distribute its jobs.
func checkForBackup() {
	time.Sleep(2 * time.Second)
	if receivedBackup {
		<-emapGuard
		for addr, elev := range backup {
			if _, ok := emap[addr]; !ok {
				//found an elevator in the backup that is not connected now
				DistributeJobs(elev)
			}
		}
		emapGuard <- true
	}
}

func sendLights(newConn *net.TCPConn){
	for _,elev := range emap{
		for i := 0; i < N_FLOORS; i++{
			if (elev.Uprun[i] == CALL){
				SendMessage(newConn, Event{TURN_ON_UP_LIGHT, i});
			}
			if (elev.Downrun[i] == CALL){
				SendMessage(newConn, Event{TURN_ON_DOWN_LIGHT, i});
			}
		}
	}
}

func main() {
	//unlock the mutex
	emapGuard <- true

	//Flag and variable for backup if all elevators are lost
	DistributeBU := false
	BUelev := ElevatorStruct{}

	//channels to communicate with network module
	newconnChan := make(chan *net.TCPConn)
	lostConnChan := make(chan *net.TCPConn)
	msgChan := make(chan Message)

	//start the TCP server in its own thread
	go StartTCPServer(":10002", newconnChan, lostConnChan, msgChan)

	//start the UDP broadcasting in its own thread
	go BroadcastUDP("129.241.187.255:10001")

	//start thread to handle messages from slaves
	go handleMessages(msgChan)

	//start thread that handles the backup (if received)
	go checkForBackup()

	//Wait for something to happen...
	for {
		select {
		//for each new connection, make a new entry in the elevatorMap
		case newConn := <-newconnChan:
			<-emapGuard
			addr := newConn.RemoteAddr().String()
			addr = addr[0:strings.Index(addr, ":")]
			emap[addr] = ElevatorStruct{[4]int{0, 0, 0, 0}, [4]int{0, 0, 0, 0}, 0, 0, newConn}
			fmt.Println("Number of connections: ", len(emap))
			SendMessage(newConn, len(emap))
			sendLights(newConn);
			if DistributeBU {
				DistributeJobs(BUelev)
				DistributeBU = false
				fmt.Println("BU sent: ", BUelev)
			}
			emapGuard <- true
			break
		//for each lost connection, distribute that elevators jobs and delete entry in elevatormap
		case lostConn := <-lostConnChan:
			<-emapGuard
			addr := lostConn.RemoteAddr().String()
			addr = addr[0:strings.Index(addr, ":")]
			elev := emap[addr]
			delete(emap, addr)
			fmt.Println("Number of connections: ", len(emap))
			if len(emap) == 0 {
				DistributeBU = true
				BUelev = elev
			} else {
				DistributeJobs(elev)
				RemakeQueue()
			}
			emapGuard <- true
			break
		}
	}
}
