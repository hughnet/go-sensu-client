package sensu

import (
	"fmt"
	"github.com/streadway/amqp"
	"log"
)

type Subscriber struct {
	deliveries <-chan amqp.Delivery
	done       chan error
}

func (s *Subscriber) Init(q MessageQueuer, c *Config) error {

	config_name, _ := c.Data().Get("client").Get("name").String()
	config_ver, _ := c.Data().Get("client").Get("version").String()

	queue_name := config_name + "-" + config_ver
	log.Printf("Declaring Queue: %s", queue_name)
	queue, err := q.QueueDeclare(queue_name)
	if err != nil {
		return fmt.Errorf("Queue Declare: %s", err)
	}
	log.Printf("declared Queue")

	var subscriptions []string
	subscriptions, err = c.Data().GetPath("client", "subscriptions").StringArray()
	if err != nil {
		return fmt.Errorf("Subscriptions are not in a string array format")
	}

	for _, sub := range subscriptions {
		log.Printf("declaring Exchange (%q)", sub)
		err = q.ExchangeDeclare(sub, "fanout")
		if err != nil {
			return fmt.Errorf("Exchange Declare: %s", err)
		}

		log.Printf("binding %s to Exchange %q", queue.Name, sub)
		err = q.QueueBind(queue.Name, "", sub)
		if err != nil {
			return fmt.Errorf("Queue Bind: %s", err)
		}
	}

	log.Printf("starting Consume")
	s.deliveries, err = q.Consume(queue.Name, "")
	if err != nil {
		return fmt.Errorf("Queue Consume: %s", err)
	}

	s.done = make(chan error)
	return nil
}

func (s *Subscriber) Start() {
	go handle(s.deliveries, s.done)

	// for {
	// 	select {
	// 	case <-s.done:
	// 		return
	// 	}
	// }
}

func (s *Subscriber) Stop() {
	s.done <- nil
}

func handle(deliveries <-chan amqp.Delivery, done chan error) {
	for d := range deliveries {
		log.Printf(
			"got %dB delivery: [%v] %q",
			len(d.Body),
			d.DeliveryTag,
			d.Body,
		)
	}
	log.Printf("handle: deliveries channel closed")
	done <- nil
}
