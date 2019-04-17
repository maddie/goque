package goque

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/dgraph-io/badger/v3"
)

// Queue is a standard FIFO (first in, first out) queue.
type Queue struct {
	sync.RWMutex
	DataDir string
	db      *badger.DB
	head    uint64
	tail    uint64
	isOpen  bool
}

// OpenQueue opens a queue if one exists at the given directory. If one
// does not already exist, a new queue is created.
func OpenQueue(dataDir string) (*Queue, error) {
	var err error

	// Create a new Queue.
	q := &Queue{
		DataDir: dataDir,
		db:      &badger.DB{},
		head:    0,
		tail:    0,
		isOpen:  false,
	}

	// Open database for the queue.
	opt := badger.DefaultOptions(dataDir)
	opt = opt.WithValueLogFileSize(1 << 24)

	q.db, err = badger.Open(opt)
	if err != nil {
		return q, err
	}

	// Check if this Goque type can open the requested data directory.
	ok, err := checkGoqueType(dataDir, goqueQueue)
	if err != nil {
		return q, err
	}
	if !ok {
		return q, ErrIncompatibleType
	}

	// Set isOpen and return.
	q.isOpen = true
	return q, q.init()
}

// OpenQueueWithOptions opens a queue with given BadgerDB options at the directory
// defined within the options. If one does not already exist, a new queue is created.
func OpenQueueWithOptions(options badger.Options) (*Queue, error) {
	var err error

	// Create a new Queue.
	q := &Queue{
		DataDir: options.Dir,
		db:      &badger.DB{},
		head:    0,
		tail:    0,
		isOpen:  false,
	}

	// Open database for the queue.
	q.db, err = badger.Open(options)
	if err != nil {
		return q, err
	}

	// Check if this Goque type can open the requested data directory.
	ok, err := checkGoqueType(options.Dir, goqueQueue)
	if err != nil {
		return q, err
	}
	if !ok {
		return q, ErrIncompatibleType
	}

	// Set isOpen and return.
	q.isOpen = true
	return q, q.init()
}

// Enqueue adds an item to the queue.
func (q *Queue) Enqueue(value []byte) (*Item, error) {
	q.Lock()
	defer q.Unlock()

	// Check if queue is closed.
	if !q.isOpen {
		return nil, ErrDBClosed
	}

	// Create new Item.
	item := &Item{
		ID:    q.tail + 1,
		Key:   idToKey(q.tail + 1),
		Value: value,
	}

	// Add it to the queue.
	if err := q.db.Update(func(txn *badger.Txn) error {
		err := txn.Set(item.Key, item.Value)
		return err
	}); err != nil {
		return nil, err
	}

	// Increment tail position.
	q.tail++

	return item, nil
}

// EnqueueString is a helper function for Enqueue that accepts a
// value as a string rather than a byte slice.
func (q *Queue) EnqueueString(value string) (*Item, error) {
	return q.Enqueue([]byte(value))
}

// EnqueueObject is a helper function for Enqueue that accepts any
// value type, which is then encoded into a byte slice using
// encoding/gob.
//
// Objects containing pointers with zero values will decode to nil
// when using this function. This is due to how the encoding/gob
// package works. Because of this, you should only use this function
// to encode simple types.
func (q *Queue) EnqueueObject(value interface{}) (*Item, error) {
	var buffer bytes.Buffer
	enc := gob.NewEncoder(&buffer)
	if err := enc.Encode(value); err != nil {
		return nil, err
	}

	return q.Enqueue(buffer.Bytes())
}

// EnqueueObjectAsJSON is a helper function for Enqueue that accepts
// any value type, which is then encoded into a JSON byte slice using
// encoding/json.
//
// Use this function to handle encoding of complex types.
func (q *Queue) EnqueueObjectAsJSON(value interface{}) (*Item, error) {
	jsonBytes, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}

	return q.Enqueue(jsonBytes)
}

// Dequeue removes the next item in the queue and returns it.
func (q *Queue) Dequeue() (*Item, error) {
	q.Lock()
	defer q.Unlock()

	// Check if queue is closed.
	if !q.isOpen {
		return nil, ErrDBClosed
	}

	// Try to get the next item in the queue.
	item, err := q.getItemByID(q.head + 1)
	if err != nil {
		return nil, err
	}

	// Remove this item from the queue.
	if err := q.db.Update(func(txn *badger.Txn) error {
		return txn.Delete(item.Key)
	}); err != nil {
		return nil, err
	}

	// Increment head position.
	q.head++

	return item, nil
}

// Peek returns the next item in the queue without removing it.
func (q *Queue) Peek() (*Item, error) {
	q.RLock()
	defer q.RUnlock()

	// Check if queue is closed.
	if !q.isOpen {
		return nil, ErrDBClosed
	}

	return q.getItemByID(q.head + 1)
}

// PeekByOffset returns the item located at the given offset,
// starting from the head of the queue, without removing it.
func (q *Queue) PeekByOffset(offset uint64) (*Item, error) {
	q.RLock()
	defer q.RUnlock()

	// Check if queue is closed.
	if !q.isOpen {
		return nil, ErrDBClosed
	}

	return q.getItemByID(q.head + offset + 1)
}

// PeekByID returns the item with the given ID without removing it.
func (q *Queue) PeekByID(id uint64) (*Item, error) {
	q.RLock()
	defer q.RUnlock()

	// Check if queue is closed.
	if !q.isOpen {
		return nil, ErrDBClosed
	}

	return q.getItemByID(id)
}

// Update updates an item in the queue without changing its position.
func (q *Queue) Update(id uint64, newValue []byte) (*Item, error) {
	q.Lock()
	defer q.Unlock()

	// Check if queue is closed.
	if !q.isOpen {
		return nil, ErrDBClosed
	}

	// Check if item exists in queue.
	if id <= q.head || id > q.tail {
		return nil, ErrOutOfBounds
	}

	// Create new Item.
	item := &Item{
		ID:    id,
		Key:   idToKey(id),
		Value: newValue,
	}

	// Update this item in the queue.
	if err := q.db.Update(func(txn *badger.Txn) error {
		return txn.Set(item.Key, item.Value)
	}); err != nil {
		return nil, err
	}

	return item, nil
}

// UpdateString is a helper function for Update that accepts a value
// as a string rather than a byte slice.
func (q *Queue) UpdateString(id uint64, newValue string) (*Item, error) {
	return q.Update(id, []byte(newValue))
}

// UpdateObject is a helper function for Update that accepts any
// value type, which is then encoded into a byte slice using
// encoding/gob.
//
// Objects containing pointers with zero values will decode to nil
// when using this function. This is due to how the encoding/gob
// package works. Because of this, you should only use this function
// to encode simple types.
func (q *Queue) UpdateObject(id uint64, newValue interface{}) (*Item, error) {
	var buffer bytes.Buffer
	enc := gob.NewEncoder(&buffer)
	if err := enc.Encode(newValue); err != nil {
		return nil, err
	}
	return q.Update(id, buffer.Bytes())
}

// UpdateObjectAsJSON is a helper function for Update that accepts
// any value type, which is then encoded into a JSON byte slice using
// encoding/json.
//
// Use this function to handle encoding of complex types.
func (q *Queue) UpdateObjectAsJSON(id uint64, newValue interface{}) (*Item, error) {
	jsonBytes, err := json.Marshal(newValue)
	if err != nil {
		return nil, err
	}

	return q.Update(id, jsonBytes)
}

// Length returns the total number of items in the queue.
func (q *Queue) Length() uint64 {
	return q.tail - q.head
}

// Close closes the BadgerDB database of the queue.
func (q *Queue) Close() error {
	q.Lock()
	defer q.Unlock()

	// Check if queue is already closed.
	if !q.isOpen {
		return nil
	}

	// Close the BadgerDB database.
	if err := q.db.Close(); err != nil {
		return err
	}

	// Reset queue head and tail and set
	// isOpen to false.
	q.head = 0
	q.tail = 0
	q.isOpen = false

	return nil
}

// Drop closes and deletes the BadgerDB database of the queue.
func (q *Queue) Drop() error {
	if err := q.Close(); err != nil {
		return err
	}

	return os.RemoveAll(q.DataDir)
}

// RunGC runs garbage collection on database and discard deleleted data from value log
func (q *Queue) RunGC(discardRatio float64) error {
	return q.db.RunValueLogGC(discardRatio)
}

// getItemByID returns an item, if found, for the given ID.
func (q *Queue) getItemByID(id uint64) (*Item, error) {
	// Check if empty or out of bounds.
	if q.Length() == 0 {
		return nil, ErrEmpty
	} else if id <= q.head || id > q.tail {
		return nil, ErrOutOfBounds
	}

	// Get item from database.
	var err error
	item := &Item{ID: id, Key: idToKey(id)}
	if err = q.db.View(func(txn *badger.Txn) error {
		badgerItem, err := txn.Get(item.Key)
		if err != nil {
			return err
		}

		if returnedValue, err := badgerItem.ValueCopy(nil); err != nil {
			return err
		} else {
			item.Value = returnedValue
		}

		return nil
	}); err != nil {
		return nil, err
	}

	return item, nil
}

// init initializes the queue data.
func (q *Queue) init() error {
	return q.db.View(func(txn *badger.Txn) error {
		// Create a new BadgerDB Iterator.
		opts := badger.DefaultIteratorOptions
		it := txn.NewIterator(opts)
		defer it.Close()

		if it.Rewind(); it.Valid() {
			// Set queue head to the first item.
			q.head = keyToID(it.Item().Key()) - 1

			for {
				if it.Next(); it.Valid() {
					// Set queue tail to the last item.
					q.tail = keyToID(it.Item().Key())
				} else {
					break
				}
			}
		}

		// reinitialize queue in case of database error
		if q.head > q.tail {
			q.head = 0
			q.tail = 0
			if err := q.db.DropAll(); err != nil {
				fmt.Printf("error resetting database: %s", err)
			}
		}

		return nil
	})
}
