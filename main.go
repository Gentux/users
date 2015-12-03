package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	_ "github.com/lib/pq"
	"github.com/natefinch/pie"
	"github.com/streadway/amqp"
	"log"
	"net/http"
	"net/rpc/jsonrpc"
	"net/url"
	"os"
	"regexp"
)

var (
	name = "users"
	srv  pie.Server
)

type api struct{}

var PgDb *sql.DB

type UserInfo struct {
	Name      string
	Email     string
	Password  string
	Activated string
	Sam       string
}

type Message struct {
	Method    string
	Name      string
	Email     string
	Activated string
	Sam       string
	Password  string
}

type PlugRequest struct {
	Body     string
	Header   http.Header
	Form     url.Values
	PostForm url.Values
	Url      string
	Method   string
	HeadVals map[string]string
	Status   int
}

type ReturnMsg struct {
	Method string
	Err    string
	Plugin string
	Email  string
}

func GetList(users *[]UserInfo) (err error) {
	rows, err := PgDb.Query("SELECT * FROM users")
	if err != nil {
		return
	}

	defer rows.Close()
	for rows.Next() {
		user := UserInfo{}

		rows.Scan(&user.Name, &user.Email, &user.Password, &user.Activated, &user.Sam)
		*users = append(*users, user)
	}

	err = rows.Err()
	return
}

func GetUser(args PlugRequest, reply *PlugRequest, mail string) (err error) {
	reply.Status = 400
	if mail == "" {
		err = errors.New(fmt.Sprintf("Email needed to retrieve account informations"))

		log.Println(err)
		return
	}

	reply.Status = 500
	rows, err := PgDb.Query(
		`SELECT name, email, password, activated
		FROM users WHERE email = $1::varchar`,
		mail)
	if err != nil {
		return
	}

	defer rows.Close()
	if rows.Next() {
		reply.Status = 200

		reply.HeadVals = make(map[string]string, 1)
		reply.HeadVals["Content-Type"] = "application/json;charset=UTF-8"

		var activated bool
		var user UserInfo
		rows.Scan(
			&user.Name, &user.Email,
			&user.Password, &activated,
		)
		if activated {
			user.Activated = "true"
		} else {
			user.Activated = "false"
		}

		var res []byte
		res, err = json.Marshal(user)
		if err != nil {
			reply.Status = 500
			return
		}

		reply.Status = 200
		reply.Body = string(res)
	} else {
		reply.Status = 404
		err = errors.New(fmt.Sprintf("User Not Found"))
	}

	return
}

func failOnError(err error, msg string) {
	if err != nil {
		log.Fatalf("%s: %s", msg, err)
		panic(fmt.Sprintf("%s: %s", msg, err))
	}
}

func SendMsg(msg Message) {
	conn, err := amqp.Dial("amqp://guest:guest@localhost:5672/")
	failOnError(err, "Failed to connect to RabbitMQ")

	ch, err := conn.Channel()
	failOnError(err, "Failed to open a channel")
	err = ch.ExchangeDeclare(
		"users_topic", // name
		"topic",       // type
		true,          // durable
		false,         // auto-deleted
		false,         // internal
		false,         // no-wait
		nil,           // arguments
	)
	failOnError(err, "Failed to declare an exchange")
	str, err := json.Marshal(msg)
	if err != nil {
		log.Println(err)
	}
	err = ch.Publish(
		"users_topic", // exchange
		"users.req",   // routing key
		false,         // mandatory
		false,         // immediate
		amqp.Publishing{
			ContentType: "application/json",
			Body:        []byte(str),
		})
	failOnError(err, "Failed to publish a message")

	log.Printf(" [x] Sent order to plugin")
	defer ch.Close()
	defer conn.Close()

}

func Add(args PlugRequest, reply *PlugRequest, mail string) (err error) {
	var t UserInfo
	err = json.Unmarshal([]byte(args.Body), &t)
	if err != nil {
		log.Println(err)
		return
	}

	rows, err := PgDb.Query(
		`INSERT INTO users
			(name, email, password,
			activated, sam)
			VALUES (
				$1::varchar,
				$2::varchar,
				$3::varchar,
				$4::bool,
				''
			)`,
		t.Name, t.Email,
		t.Password, (t.Activated == "true"))

	if err != nil {
		switch err.Error() {
		case "pq: duplicate key value violates unique constraint \"users_pkey\"":
			err = errors.New("user email exists already")

			/*
					TODO: integrate user uuid
				case "pq: duplicate key value violates unique constraint \"users_pkey\"":
					err = errors.New("user id exists already")
				case "pq: duplicate key value violates unique constraint \"users_email_key\"":
					err = errors.New("user email exists already")
			*/
		}
		return
	}

	rows.Close()

	rows, err = PgDb.Query(
		`SELECT name, email,
		password, activated
		FROM users
		WHERE email = $1::varchar`,
		t.Email)

	if err != nil {
		return
	}

	if !rows.Next() {
		err = errors.New("user not created")
		return
	}

	var user UserInfo
	var activated bool
	rows.Scan(
		&user.Name, &user.Email,
		&user.Password, &activated, &user.Activated,
	)
	if activated {
		user.Activated = "true"
	} else {
		user.Activated = "false"
	}

	rows.Close()

	SendMsg(Message{Method: "Add", Name: user.Name, Email: user.Email, Password: user.Password, Activated: user.Activated})

	reply.HeadVals = make(map[string]string, 1)
	reply.HeadVals["Content-Type"] = "text/html;charset=UTF-8"
	if err == nil {
		reply.Status = 202
	} else {
		reply.Status = 400
	}
	return
}

func ModifyPassword(args PlugRequest, reply *PlugRequest, mail string) (err error) {
	reply.Status = 400
	if mail == "" {
		err = errors.New(fmt.Sprintf("Email needed to modify account"))
		return
	}
	reply.Status = 500

	var t UserInfo
	err = json.Unmarshal([]byte(args.Body), &t)
	if err != nil {
		log.Println(err)
		return
	}

	rows, err := PgDb.Query(
		`UPDATE users
		SET password = $1::varchar
		WHERE email = $2::varchar`,
		t.Password, mail)

	if err != nil {
		return
	}
	rows.Close()

	reply.Status = 202
	SendMsg(Message{Method: "ChangePassword", Name: t.Name, Password: t.Password, Email: mail})
	return
}

func DisableAccount(args PlugRequest, reply *PlugRequest, mail string) (err error) {
	reply.Status = 404
	if mail == "" {
		err = errors.New(fmt.Sprintf("Email needed for desactivation"))
		return
	}

	reply.Status = 500
	rows, err := PgDb.Query(
		`UPDATE users
		SET activated = false
		WHERE email = $1::varchar`,
		mail)

	if err != nil {
		return
	}
	rows.Close()
	reply.Status = 202

	return
}

func Delete(args PlugRequest, reply *PlugRequest, mail string) (err error) {
	if mail == "" {
		reply.Status = 400
		err = errors.New(fmt.Sprintf("Email needed for deletion"))
		return
	}
	reply.Status = 500

	rows, err := PgDb.Query("DELETE FROM users WHERE email = $1::varchar", mail)

	if err != nil {
		return
	}
	rows.Close()
	SendMsg(Message{Method: "Delete", Email: mail})

	reply.Status = 202

	return
}

func ListCall(args PlugRequest, reply *PlugRequest, mail string) error {
	var users []UserInfo
	GetList(&users)
	rsp, err := json.Marshal(users)
	reply.Body = string(rsp)
	reply.HeadVals = make(map[string]string, 1)
	reply.HeadVals["Content-Type"] = "application/json;charset=UTF-8"
	if err == nil {
		reply.Status = 200
	} else {
		reply.Status = 400
	}
	return nil
}

var tab = []struct {
	Url    string
	Method string
	f      func(PlugRequest, *PlugRequest, string) error
}{
	{`^\/api\/users\/(?P<id>[^\/]+)\/disable\/{0,1}$`, "POST", DisableAccount},
	{`^\/api\/users\/{0,1}$`, "GET", ListCall},
	{`^\/api\/users\/{0,1}$`, "POST", Add},
	{`^\/api\/users\/(?P<id>[^\/]+)\/{0,1}$`, "DELETE", Delete},
	{`^\/api\/users\/(?P<id>[^\/]+)\/{0,1}$`, "PUT", ModifyPassword},
	{`^\/api\/users\/(?P<id>[^\/]+)\/{0,1}$`, "GET", GetUser},
}

func (api) Receive(args PlugRequest, reply *PlugRequest) error {
	for _, val := range tab {
		re := regexp.MustCompile(val.Url)
		match := re.MatchString(args.Url)
		if val.Method == args.Method && match {
			fmt.Fprintf(os.Stderr, ">> %s\n", val.Url)
			if len(re.FindStringSubmatch(args.Url)) == 2 {
				err := val.f(args, reply, re.FindStringSubmatch(args.Url)[1])
				if err != nil {
					log.Println(err)
				}
			} else {
				err := val.f(args, reply, "")

				if err != nil {
					log.Println(err)
				}
			}
		}
	}
	return nil
}

type Queue struct {
	Name string
}

func ListenToQueue() {
	conn, err := amqp.Dial(conf.QueueUri)
	failOnError(err, "Failed to connect to RabbitMQ")
	//defer conn.Close()

	ch, err := conn.Channel()
	failOnError(err, "Failed to open a channel")

	err = ch.ExchangeDeclare(
		"users_topic", // name
		"topic",       // type
		true,          // durable
		false,         // auto-deleted
		false,         // internal
		false,         // no-wait
		nil,           // arguments
	)
	failOnError(err, "Failed to declare an exchange")
	_, err = ch.QueueDeclare(
		"users", // name
		false,   // durable
		false,   // delete when usused
		false,   // exclusive
		false,   // no-wait
		nil,     // arguments
	)
	failOnError(err, "Failed to declare an queue")
	err = ch.QueueBind(
		"users",       // queue name
		"*.users",     // routing key
		"users_topic", // exchange
		false,
		nil)
	failOnError(err, "Failed to bind a queue")
	responses, err := ch.Consume(
		"users", // queue
		"",      // consumer
		true,    // auto-ack
		false,   // exclusive
		false,   // no-local
		false,   // no-wait
		nil,     // args
	)
	failOnError(err, "Failed to register a consumer")
	forever := make(chan bool)
	go func() {
		for d := range responses {
			HandleReturns(d.Body)
		}
	}()
	log.Println("Waiting for responses of other plugins")
	defer ch.Close()
	defer conn.Close()
	<-forever
}

func HandleReturns(ret []byte) {
	var Msg ReturnMsg
	err := json.Unmarshal(ret, &Msg)
	if err != nil {
		log.Println(err)
	}
	if Msg.Err == "" {
		log.Println("Request:", Msg.Method, "Successfully completed by plugin", Msg.Plugin)
	} else {
		if Msg.Method == "Add" {
			log.Println("Request:", Msg.Method, "Didn't complete by plugin", Msg.Plugin, ", now reversing process")
			Delete(PlugRequest{}, &PlugRequest{}, Msg.Email)
		} else {
			log.Println("Request:", Msg.Method, "Didn't complete by plugin", Msg.Plugin)
		}
	}
}

func (api) Plug(args interface{}, reply *bool) error {
	go ListenToQueue()
	*reply = true
	return nil
}

func (api) Check(args interface{}, reply *bool) error {
	*reply = true
	return nil
}

func (api) Unplug(args interface{}, reply *bool) error {
	defer os.Exit(0)
	*reply = true
	return nil
}

func main() {
	var err error

	srv = pie.NewProvider()

	if err = srv.RegisterName(name, api{}); err != nil {
		log.Fatalf("Failed to register %s: %s", name, err)
	}

	initConf()

	PgDb, err = sql.Open("postgres", conf.DatabaseUri)
	if err != nil {
		log.Fatalf("Cannot connect to Postgres Database: %s", err)
	}

	srv.ServeCodec(jsonrpc.NewServerCodec)
}