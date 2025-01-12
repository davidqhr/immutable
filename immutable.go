// Package immutable provides immutable collection types.
//
// Introduction
//
// Immutable collections provide an efficient, safe way to share collections
// of data while minimizing locks. The collections in this package provide
// List, Map, and SortedMap implementations. These act similarly to slices
// and maps, respectively, except that altering a collection returns a new
// copy of the collection with that change.
//
// Because collections are unable to change, they are safe for multiple
// goroutines to read from at the same time without a mutex. However, these
// types of collections come with increased CPU & memory usage as compared
// with Go's built-in collection types so please evaluate for your specific
// use.
//
// Collection Types
//
// The List type provides an API similar to Go slices. They allow appending,
// prepending, and updating of elements. Elements can also be fetched by index
// or iterated over using a ListIterator.
//
// The Map & SortedMap types provide an API similar to Go maps. They allow
// values to be assigned to unique keys and allow for the deletion of keys.
// Values can be fetched by key and key/value pairs can be iterated over using
// the appropriate iterator type. Both map types provide the same API. The
// SortedMap, however, provides iteration over sorted keys while the Map
// provides iteration over unsorted keys. Maps improved performance and memory
// usage as compared to SortedMaps.
//
// Hashing and Sorting
//
// Map types require the use of a Hasher implementation to calculate hashes for
// their keys and check for key equality. SortedMaps require the use of a
// Comparer implementation to sort keys in the map.
//
// These collection types automatically provide built-in hasher and comparers
// for int, string, and byte slice keys. If you are using one of these key types
// then simply pass a nil into the constructor. Otherwise you will need to
// implement a custom Hasher or Comparer type. Please see the provided
// implementations for reference.
package immutable

import (
	"fmt"
	"math/bits"
	"reflect"
)

// List is a dense, ordered, indexed collections. They are analogous to slices
// in Go. They can be updated by appending to the end of the list, prepending
// values to the beginning of the list, or updating existing indexes in the
// list.
type List[T comparable] struct {
	root   listNode[T] // root node
	origin int         // offset to zero index element
	size   int         // total number of elements in use
}

// NewList returns a new empty instance of List.
func NewList[T comparable]() *List[T] {
	return &List[T]{
		root: &listLeafNode[T]{},
	}
}

// clone returns a copy of the list.
func (l *List[T]) clone() *List[T] {
	other := *l
	return &other
}

// Len returns the number of elements in the list.
func (l *List[T]) Len() int {
	return l.size
}

// cap returns the total number of possible elements for the current depth.
func (l *List[T]) cap() int {
	return 1 << (l.root.depth() * listNodeBits)
}

// Get returns the value at the given index. Similar to slices, this method will
// panic if index is below zero or is greater than or equal to the list size.
func (l *List[T]) Get(index int) T {
	if index < 0 || index >= l.size {
		panic(fmt.Sprintf("immutable.List.Get: index %d out of bounds", index))
	}
	return l.root.get(l.origin + index)
}

// Set returns a new list with value set at index. Similar to slices, this
// method will panic if index is below zero or if the index is greater than
// or equal to the list size.
func (l *List[T]) Set(index int, value T) *List[T] {
	return l.set(index, value, false)
}

func (l *List[T]) set(index int, value T, mutable bool) *List[T] {
	if index < 0 || index >= l.size {
		panic(fmt.Sprintf("immutable.List.Set: index %d out of bounds", index))
	}
	other := l
	if !mutable {
		other = l.clone()
	}
	other.root = other.root.set(l.origin+index, value, mutable)
	return other
}

// Append returns a new list with value added to the end of the list.
func (l *List[T]) Append(value T) *List[T] {
	return l.append(value, false)
}

func (l *List[T]) append(value T, mutable bool) *List[T] {
	other := l
	if !mutable {
		other = l.clone()
	}

	// Expand list to the right if no slots remain.
	if other.size+other.origin >= l.cap() {
		newRoot := &listBranchNode[T]{d: other.root.depth() + 1}
		newRoot.children[0] = other.root
		other.root = newRoot
	}

	// Increase size and set the last element to the new value.
	other.size++
	other.root = other.root.set(other.origin+other.size-1, value, mutable)
	return other
}

// Prepend returns a new list with value added to the beginning of the list.
func (l *List[T]) Prepend(value T) *List[T] {
	return l.prepend(value, false)
}

func (l *List[T]) prepend(value T, mutable bool) *List[T] {
	other := l
	if !mutable {
		other = l.clone()
	}

	// Expand list to the left if no slots remain.
	if other.origin == 0 {
		newRoot := &listBranchNode[T]{d: other.root.depth() + 1}
		newRoot.children[listNodeSize-1] = other.root
		other.root = newRoot
		other.origin += (listNodeSize - 1) << (other.root.depth() * listNodeBits)
	}

	// Increase size and move origin back. Update first element to value.
	other.size++
	other.origin--
	other.root = other.root.set(other.origin, value, mutable)
	return other
}

// Slice returns a new list of elements between start index and end index.
// Similar to slices, this method will panic if start or end are below zero or
// greater than the list size. A panic will also occur if start is greater than
// end.
//
// Unlike Go slices, references to inaccessible elements will be automatically
// removed so they can be garbage collected.
func (l *List[T]) Slice(start, end int) *List[T] {
	return l.slice(start, end, false)
}

func (l *List[T]) slice(start, end int, mutable bool) *List[T] {
	// Panics similar to Go slices.
	if start < 0 || start > l.size {
		panic(fmt.Sprintf("immutable.List.Slice: start index %d out of bounds", start))
	} else if end < 0 || end > l.size {
		panic(fmt.Sprintf("immutable.List.Slice: end index %d out of bounds", end))
	} else if start > end {
		panic(fmt.Sprintf("immutable.List.Slice: invalid slice index: [%d:%d]", start, end))
	}

	// Return the same list if the start and end are the entire range.
	if start == 0 && end == l.size {
		return l
	}

	// Create copy, if immutable.
	other := l
	if !mutable {
		other = l.clone()
	}

	// Update origin/size.
	other.origin = l.origin + start
	other.size = end - start

	// Contract tree while the start & end are in the same child node.
	for other.root.depth() > 1 {
		i := (other.origin >> (other.root.depth() * listNodeBits)) & listNodeMask
		j := ((other.origin + other.size - 1) >> (other.root.depth() * listNodeBits)) & listNodeMask
		if i != j {
			break // branch contains at least two nodes, exit
		}

		// Replace the current root with the single child & update origin offset.
		other.origin -= i << (other.root.depth() * listNodeBits)
		other.root = other.root.(*listBranchNode[T]).children[i]
	}

	// Ensure all references are removed before start & after end.
	other.root = other.root.deleteBefore(other.origin, mutable)
	other.root = other.root.deleteAfter(other.origin+other.size-1, mutable)

	return other
}

// Iterator returns a new iterator for this list positioned at the first index.
func (l *List[T]) Iterator() *ListIterator[T] {
	itr := &ListIterator[T]{list: l}
	itr.First()
	return itr
}

// ListBuilder represents an efficient builder for creating new Lists.
type ListBuilder[T comparable] struct {
	list *List[T] // current state
}

// NewListBuilder returns a new instance of ListBuilder.
func NewListBuilder[T comparable]() *ListBuilder[T] {
	return &ListBuilder[T]{list: NewList[T]()}
}

// List returns the current copy of the list.
// The builder should not be used again after the list after this call.
func (b *ListBuilder[T]) List() *List[T] {
	assert(b.list != nil, "immutable.ListBuilder.List(): duplicate call to fetch list")
	list := b.list
	b.list = nil
	return list
}

// Len returns the number of elements in the underlying list.
func (b *ListBuilder[T]) Len() int {
	assert(b.list != nil, "immutable.ListBuilder: builder invalid after List() invocation")
	return b.list.Len()
}

// Get returns the value at the given index. Similar to slices, this method will
// panic if index is below zero or is greater than or equal to the list size.
func (b *ListBuilder[T]) Get(index int) T {
	assert(b.list != nil, "immutable.ListBuilder: builder invalid after List() invocation")
	return b.list.Get(index)
}

// Set updates the value at the given index. Similar to slices, this method will
// panic if index is below zero or if the index is greater than or equal to the
// list size.
func (b *ListBuilder[T]) Set(index int, value T) {
	assert(b.list != nil, "immutable.ListBuilder: builder invalid after List() invocation")
	b.list = b.list.set(index, value, true)
}

// Append adds value to the end of the list.
func (b *ListBuilder[T]) Append(value T) {
	assert(b.list != nil, "immutable.ListBuilder: builder invalid after List() invocation")
	b.list = b.list.append(value, true)
}

// Prepend adds value to the beginning of the list.
func (b *ListBuilder[T]) Prepend(value T) {
	assert(b.list != nil, "immutable.ListBuilder: builder invalid after List() invocation")
	b.list = b.list.prepend(value, true)
}

// Slice updates the list with a sublist of elements between start and end index.
// See List.Slice() for more details.
func (b *ListBuilder[T]) Slice(start, end int) {
	assert(b.list != nil, "immutable.ListBuilder: builder invalid after List() invocation")
	b.list = b.list.slice(start, end, true)
}

// Iterator returns a new iterator for the underlying list.
func (b *ListBuilder[T]) Iterator() *ListIterator[T] {
	assert(b.list != nil, "immutable.ListBuilder: builder invalid after List() invocation")
	return b.list.Iterator()
}

// Constants for bit shifts used for levels in the List trie.
const (
	listNodeBits = 5
	listNodeSize = 1 << listNodeBits
	listNodeMask = listNodeSize - 1
)

// listNode represents either a branch or leaf node in a List.
type listNode[T comparable] interface {
	depth() uint
	get(index int) T
	set(index int, v T, mutable bool) listNode[T]

	containsBefore(index int) bool
	containsAfter(index int) bool

	deleteBefore(index int, mutable bool) listNode[T]
	deleteAfter(index int, mutable bool) listNode[T]
}

// newListNode returns a leaf node for depth zero, otherwise returns a branch node.
func newListNode[T comparable](depth uint) listNode[T] {
	if depth == 0 {
		return &listLeafNode[T]{}
	}
	return &listBranchNode[T]{d: depth}
}

// listBranchNode represents a branch of a List tree at a given depth.
type listBranchNode[T comparable] struct {
	d        uint // depth
	children [listNodeSize]listNode[T]
}

// depth returns the depth of this branch node from the leaf.
func (n *listBranchNode[T]) depth() uint { return n.d }

// get returns the child node at the segment of the index for this depth.
func (n *listBranchNode[T]) get(index int) T {
	idx := (index >> (n.d * listNodeBits)) & listNodeMask
	return n.children[idx].get(index)
}

// set recursively updates the value at index for each lower depth from the node.
func (n *listBranchNode[T]) set(index int, v T, mutable bool) listNode[T] {
	idx := (index >> (n.d * listNodeBits)) & listNodeMask

	// Find child for the given value in the branch. Create new if it doesn't exist.
	child := n.children[idx]
	if child == nil {
		child = newListNode[T](n.depth() - 1)
	}

	// Return a copy of this branch with the new child.
	var other *listBranchNode[T]
	if mutable {
		other = n
	} else {
		tmp := *n
		other = &tmp
	}
	other.children[idx] = child.set(index, v, mutable)
	return other
}

// containsBefore returns true if non-nil values exists between [0,index).
func (n *listBranchNode[T]) containsBefore(index int) bool {
	idx := (index >> (n.d * listNodeBits)) & listNodeMask

	// Quickly check if any direct children exist before this segment of the index.
	for i := 0; i < idx; i++ {
		if n.children[i] != nil {
			return true
		}
	}

	// Recursively check for children directly at the given index at this segment.
	if n.children[idx] != nil && n.children[idx].containsBefore(index) {
		return true
	}
	return false
}

// containsAfter returns true if non-nil values exists between (index,listNodeSize).
func (n *listBranchNode[T]) containsAfter(index int) bool {
	idx := (index >> (n.d * listNodeBits)) & listNodeMask

	// Quickly check if any direct children exist after this segment of the index.
	for i := idx + 1; i < len(n.children); i++ {
		if n.children[i] != nil {
			return true
		}
	}

	// Recursively check for children directly at the given index at this segment.
	if n.children[idx] != nil && n.children[idx].containsAfter(index) {
		return true
	}
	return false
}

// deleteBefore returns a new node with all elements before index removed.
func (n *listBranchNode[T]) deleteBefore(index int, mutable bool) listNode[T] {
	// Ignore if no nodes exist before the given index.
	if !n.containsBefore(index) {
		return n
	}

	// Return a copy with any nodes prior to the index removed.
	idx := (index >> (n.d * listNodeBits)) & listNodeMask

	var other *listBranchNode[T]
	if mutable {
		other = n
		for i := 0; i < idx; i++ {
			n.children[i] = nil
		}
	} else {
		other = &listBranchNode[T]{d: n.d}
		copy(other.children[idx:][:], n.children[idx:][:])
	}

	if other.children[idx] != nil {
		other.children[idx] = other.children[idx].deleteBefore(index, mutable)
	}
	return other
}

// deleteBefore returns a new node with all elements before index removed.
func (n *listBranchNode[T]) deleteAfter(index int, mutable bool) listNode[T] {
	// Ignore if no nodes exist after the given index.
	if !n.containsAfter(index) {
		return n
	}

	// Return a copy with any nodes after the index removed.
	idx := (index >> (n.d * listNodeBits)) & listNodeMask

	var other *listBranchNode[T]
	if mutable {
		other = n
		for i := idx + 1; i < len(n.children); i++ {
			n.children[i] = nil
		}
	} else {
		other = &listBranchNode[T]{d: n.d}
		copy(other.children[:idx+1], n.children[:idx+1])
	}

	if other.children[idx] != nil {
		other.children[idx] = other.children[idx].deleteAfter(index, mutable)
	}
	return other
}

// listLeafNode represents a leaf node in a List.
type listLeafNode[T comparable] struct {
	children [listNodeSize]T
}

// depth always returns 0 for leaf nodes.
func (n *listLeafNode[T]) depth() uint { return 0 }

// get returns the value at the given index.
func (n *listLeafNode[T]) get(index int) T {
	return n.children[index&listNodeMask]
}

// set returns a copy of the node with the value at the index updated to v.
func (n *listLeafNode[T]) set(index int, v T, mutable bool) listNode[T] {
	idx := index & listNodeMask
	var other *listLeafNode[T]
	if mutable {
		other = n
	} else {
		tmp := *n
		other = &tmp
	}
	other.children[idx] = v
	var otherLN listNode[T]
	otherLN = other
	return otherLN
}

// containsBefore returns true if non-nil values exists between [0,index).
func (n *listLeafNode[T]) containsBefore(index int) bool {
	idx := index & listNodeMask
	var empty T
	for i := 0; i < idx; i++ {
		if n.children[i] != empty {
			return true
		}
	}
	return false
}

// containsAfter returns true if non-nil values exists between (index,listNodeSize).
func (n *listLeafNode[T]) containsAfter(index int) bool {
	idx := index & listNodeMask
	var empty T
	for i := idx + 1; i < len(n.children); i++ {
		if n.children[i] != empty {
			return true
		}
	}
	return false
}

// deleteBefore returns a new node with all elements before index removed.
func (n *listLeafNode[T]) deleteBefore(index int, mutable bool) listNode[T] {
	if !n.containsBefore(index) {
		return n
	}

	idx := index & listNodeMask
	var other *listLeafNode[T]
	if mutable {
		other = n
		var empty T
		for i := 0; i < idx; i++ {
			other.children[i] = empty
		}
	} else {
		other = &listLeafNode[T]{}
		copy(other.children[idx:][:], n.children[idx:][:])
	}
	return other
}

// deleteBefore returns a new node with all elements before index removed.
func (n *listLeafNode[T]) deleteAfter(index int, mutable bool) listNode[T] {
	if !n.containsAfter(index) {
		return n
	}

	idx := index & listNodeMask
	var other *listLeafNode[T]
	if mutable {
		other = n
		var empty T
		for i := idx + 1; i < len(n.children); i++ {
			other.children[i] = empty
		}
	} else {
		other = &listLeafNode[T]{}
		copy(other.children[:idx+1][:], n.children[:idx+1][:])
	}
	return other
}

// ListIterator represents an ordered iterator over a list.
type ListIterator[T comparable] struct {
	list  *List[T] // source list
	index int      // current index position

	stack [32]listIteratorElem[T] // search stack
	depth int                     // stack depth
}

// Done returns true if no more elements remain in the iterator.
func (itr *ListIterator[T]) Done() bool {
	return itr.index < 0 || itr.index >= itr.list.Len()
}

// First positions the iterator on the first index.
// If source list is empty then no change is made.
func (itr *ListIterator[T]) First() {
	if itr.list.Len() != 0 {
		itr.Seek(0)
	}
}

// Last positions the iterator on the last index.
// If source list is empty then no change is made.
func (itr *ListIterator[T]) Last() {
	if n := itr.list.Len(); n != 0 {
		itr.Seek(n - 1)
	}
}

// Seek moves the iterator position to the given index in the list.
// Similar to Go slices, this method will panic if index is below zero or if
// the index is greater than or equal to the list size.
func (itr *ListIterator[T]) Seek(index int) {
	// Panic similar to Go slices.
	if index < 0 || index >= itr.list.Len() {
		panic(fmt.Sprintf("immutable.ListIterator.Seek: index %d out of bounds", index))
	}
	itr.index = index

	// Reset to the bottom of the stack at seek to the correct position.
	itr.stack[0] = listIteratorElem[T]{node: itr.list.root}
	itr.depth = 0
	itr.seek(index)
}

// Next returns the current index and its value & moves the iterator forward.
// Returns an index of -1 if the there are no more elements to return.
func (itr *ListIterator[T]) Next() (index int, value T) {
	// Exit immediately if there are no elements remaining.
	var empty T
	if itr.Done() {
		return -1, empty
	}

	// Retrieve current index & value.
	elem := &itr.stack[itr.depth]
	index, value = itr.index, elem.node.(*listLeafNode[T]).children[elem.index]

	// Increase index. If index is at the end then return immediately.
	itr.index++
	if itr.Done() {
		return index, value
	}

	// Move up stack until we find a node that has remaining position ahead.
	for ; itr.depth > 0 && itr.stack[itr.depth].index >= listNodeSize-1; itr.depth-- {
	}

	// Seek to correct position from current depth.
	itr.seek(itr.index)

	return index, value
}

// Prev returns the current index and value and moves the iterator backward.
// Returns an index of -1 if the there are no more elements to return.
func (itr *ListIterator[T]) Prev() (index int, value T) {
	// Exit immediately if there are no elements remaining.
	var empty T
	if itr.Done() {
		return -1, empty
	}

	// Retrieve current index & value.
	elem := &itr.stack[itr.depth]
	index, value = itr.index, elem.node.(*listLeafNode[T]).children[elem.index]

	// Decrease index. If index is past the beginning then return immediately.
	itr.index--
	if itr.Done() {
		return index, value
	}

	// Move up stack until we find a node that has remaining position behind.
	for ; itr.depth > 0 && itr.stack[itr.depth].index == 0; itr.depth-- {
	}

	// Seek to correct position from current depth.
	itr.seek(itr.index)

	return index, value
}

// seek positions the stack to the given index from the current depth.
// Elements and indexes below the current depth are assumed to be correct.
func (itr *ListIterator[T]) seek(index int) {
	// Iterate over each level until we reach a leaf node.
	for {
		elem := &itr.stack[itr.depth]
		elem.index = ((itr.list.origin + index) >> (elem.node.depth() * listNodeBits)) & listNodeMask

		switch node := elem.node.(type) {
		case *listBranchNode[T]:
			child := node.children[elem.index]
			itr.stack[itr.depth+1] = listIteratorElem[T]{node: child}
			itr.depth++
		case *listLeafNode[T]:
			return
		}
	}
}

// listIteratorElem represents the node and it's child index within the stack.
type listIteratorElem[T comparable] struct {
	node  listNode[T]
	index int
}

// Size thresholds for each type of branch node.
const (
	maxArrayMapSize      = 8
	maxBitmapIndexedSize = 16
)

// Segment bit shifts within the map tree.
const (
	mapNodeBits = 5
	mapNodeSize = 1 << mapNodeBits
	mapNodeMask = mapNodeSize - 1
)

// Map represents an immutable hash map implementation. The map uses a Hasher
// to generate hashes and check for equality of key values.
//
// It is implemented as an Hash Array Mapped Trie.
type Map[K comparable, V any] struct {
	size   int           // total number of key/value pairs
	root   mapNode[K, V] // root node of trie
	hasher Hasher[K]     // hasher implementation
}

// NewMap returns a new instance of Map. If hasher is nil, a default hasher
// implementation will automatically be chosen based on the first key added.
// Default hasher implementations only exist for int, string, and byte slice types.
func NewMap[K comparable, V any](hasher Hasher[K]) *Map[K, V] {
	return &Map[K, V]{
		hasher: hasher,
	}
}

// Len returns the number of elements in the map.
func (m *Map[K, V]) Len() int {
	return m.size
}

// clone returns a shallow copy of m.
func (m *Map[K, V]) clone() *Map[K, V] {
	other := *m
	return &other
}

// Get returns the value for a given key and a flag indicating whether the
// key exists. This flag distinguishes a nil value set on a key versus a
// non-existent key in the map.
func (m *Map[K, V]) Get(key K) (value V, ok bool) {
	var empty V
	if m.root == nil {
		return empty, false
	}
	keyHash := m.hasher.Hash(key)
	return m.root.get(key, 0, keyHash, m.hasher)
}

// Set returns a map with the key set to the new value. A nil value is allowed.
//
// This function will return a new map even if the updated value is the same as
// the existing value because Map does not track value equality.
func (m *Map[K, V]) Set(key K, value V) *Map[K, V] {
	return m.set(key, value, false)
}

func (m *Map[K, V]) set(key K, value V, mutable bool) *Map[K, V] {
	// Set a hasher on the first value if one does not already exist.
	hasher := m.hasher
	if hasher == nil {
		hasher = NewHasher(key)
	}

	// Generate copy if necessary.
	other := m
	if !mutable {
		other = m.clone()
	}
	other.hasher = hasher

	// If the map is empty, initialize with a simple array node.
	if m.root == nil {
		other.size = 1
		other.root = &mapArrayNode[K, V]{entries: []mapEntry[K, V]{{key: key, value: value}}}
		return other
	}

	// Otherwise copy the map and delegate insertion to the root.
	// Resized will return true if the key does not currently exist.
	var resized bool
	other.root = m.root.set(key, value, 0, hasher.Hash(key), hasher, mutable, &resized)
	if resized {
		other.size++
	}
	return other
}

// Delete returns a map with the given key removed.
// Removing a non-existent key will cause this method to return the same map.
func (m *Map[K, V]) Delete(key K) *Map[K, V] {
	return m.delete(key, false)
}

func (m *Map[K, V]) delete(key K, mutable bool) *Map[K, V] {
	// Return original map if no keys exist.
	if m.root == nil {
		return m
	}

	// If the delete did not change the node then return the original map.
	var resized bool
	newRoot := m.root.delete(key, 0, m.hasher.Hash(key), m.hasher, mutable, &resized)
	if !resized {
		return m
	}

	// Generate copy if necessary.
	other := m
	if !mutable {
		other = m.clone()
	}

	// Return copy of map with new root and decreased size.
	other.size = m.size - 1
	other.root = newRoot
	return other
}

// Iterator returns a new iterator for the map.
func (m *Map[K, V]) Iterator() *MapIterator[K, V] {
	itr := &MapIterator[K, V]{m: m}
	itr.First()
	return itr
}

// MapBuilder represents an efficient builder for creating Maps.
type MapBuilder[K comparable, V any] struct {
	m *Map[K, V] // current state
}

// NewMapBuilder returns a new instance of MapBuilder.
func NewMapBuilder[K comparable, V any](hasher Hasher[K]) *MapBuilder[K, V] {
	return &MapBuilder[K, V]{m: NewMap[K, V](hasher)}
}

// Map returns the underlying map. Only call once.
// Builder is invalid after call. Will panic on second invocation.
func (b *MapBuilder[K, V]) Map() *Map[K, V] {
	assert(b.m != nil, "immutable.SortedMapBuilder.Map(): duplicate call to fetch map")
	m := b.m
	b.m = nil
	return m
}

// Len returns the number of elements in the underlying map.
func (b *MapBuilder[K, V]) Len() int {
	assert(b.m != nil, "immutable.MapBuilder: builder invalid after Map() invocation")
	return b.m.Len()
}

// Get returns the value for the given key.
func (b *MapBuilder[K, V]) Get(key K) (value V, ok bool) {
	assert(b.m != nil, "immutable.MapBuilder: builder invalid after Map() invocation")
	return b.m.Get(key)
}

// Set sets the value of the given key. See Map.Set() for additional details.
func (b *MapBuilder[K, V]) Set(key K, value V) {
	assert(b.m != nil, "immutable.MapBuilder: builder invalid after Map() invocation")
	b.m = b.m.set(key, value, true)
}

// Delete removes the given key. See Map.Delete() for additional details.
func (b *MapBuilder[K, V]) Delete(key K) {
	assert(b.m != nil, "immutable.MapBuilder: builder invalid after Map() invocation")
	b.m = b.m.delete(key, true)
}

// Iterator returns a new iterator for the underlying map.
func (b *MapBuilder[K, V]) Iterator() *MapIterator[K, V] {
	assert(b.m != nil, "immutable.MapBuilder: builder invalid after Map() invocation")
	return b.m.Iterator()
}

// mapNode represents any node in the map tree.
type mapNode[K comparable, V any] interface {
	get(key K, shift uint, keyHash uint32, h Hasher[K]) (value V, ok bool)
	set(key K, value V, shift uint, keyHash uint32, h Hasher[K], mutable bool, resized *bool) mapNode[K, V]
	delete(key K, shift uint, keyHash uint32, h Hasher[K], mutable bool, resized *bool) mapNode[K, V]
}

var _ mapNode[string, any] = (*mapArrayNode[string, any])(nil)
var _ mapNode[string, any] = (*mapBitmapIndexedNode[string, any])(nil)
var _ mapNode[string, any] = (*mapHashArrayNode[string, any])(nil)
var _ mapNode[string, any] = (*mapValueNode[string, any])(nil)
var _ mapNode[string, any] = (*mapHashCollisionNode[string, any])(nil)

// mapLeafNode represents a node that stores a single key hash at the leaf of the map tree.
type mapLeafNode[K comparable, V any] interface {
	mapNode[K, V]
	keyHashValue() uint32
}

var _ mapLeafNode[string, any] = (*mapValueNode[string, any])(nil)
var _ mapLeafNode[string, any] = (*mapHashCollisionNode[string, any])(nil)

// mapArrayNode is a map node that stores key/value pairs in a slice.
// Entries are stored in insertion order. An array node expands into a bitmap
// indexed node once a given threshold size is crossed.
type mapArrayNode[K comparable, V any] struct {
	entries []mapEntry[K, V]
}

// indexOf returns the entry index of the given key. Returns -1 if key not found.
func (n *mapArrayNode[K, V]) indexOf(key K, h Hasher[K]) int {
	for i := range n.entries {
		if h.Equal(n.entries[i].key, key) {
			return i
		}
	}
	return -1
}

// get returns the value for the given key.
func (n *mapArrayNode[K, V]) get(key K, shift uint, keyHash uint32, h Hasher[K]) (value V, ok bool) {
	i := n.indexOf(key, h)
	if i == -1 {
		return value, false
	}
	return n.entries[i].value, true
}

// set inserts or updates the value for a given key. If the key is inserted and
// the new size crosses the max size threshold, a bitmap indexed node is returned.
func (n *mapArrayNode[K, V]) set(key K, value V, shift uint, keyHash uint32, h Hasher[K], mutable bool, resized *bool) mapNode[K, V] {
	idx := n.indexOf(key, h)

	// Mark as resized if the key doesn't exist.
	if idx == -1 {
		*resized = true
	}

	// If we are adding and it crosses the max size threshold, expand the node.
	// We do this by continually setting the entries to a value node and expanding.
	if idx == -1 && len(n.entries) >= maxArrayMapSize {
		var node mapNode[K, V] = newMapValueNode(h.Hash(key), key, value)
		for _, entry := range n.entries {
			node = node.set(entry.key, entry.value, 0, h.Hash(entry.key), h, false, resized)
		}
		return node
	}

	// Update in-place if mutable.
	if mutable {
		if idx != -1 {
			n.entries[idx] = mapEntry[K, V]{key, value}
		} else {
			n.entries = append(n.entries, mapEntry[K, V]{key, value})
		}
		return n
	}

	// Update existing entry if a match is found.
	// Otherwise append to the end of the element list if it doesn't exist.
	var other mapArrayNode[K, V]
	if idx != -1 {
		other.entries = make([]mapEntry[K, V], len(n.entries))
		copy(other.entries, n.entries)
		other.entries[idx] = mapEntry[K, V]{key, value}
	} else {
		other.entries = make([]mapEntry[K, V], len(n.entries)+1)
		copy(other.entries, n.entries)
		other.entries[len(other.entries)-1] = mapEntry[K, V]{key, value}
	}
	return &other
}

// delete removes the given key from the node. Returns the same node if key does
// not exist. Returns a nil node when removing the last entry.
func (n *mapArrayNode[K, V]) delete(key K, shift uint, keyHash uint32, h Hasher[K], mutable bool, resized *bool) mapNode[K, V] {
	idx := n.indexOf(key, h)

	// Return original node if key does not exist.
	if idx == -1 {
		return n
	}
	*resized = true

	// Return nil if this node will contain no nodes.
	if len(n.entries) == 1 {
		return nil
	}

	// Update in-place, if mutable.
	if mutable {
		copy(n.entries[idx:], n.entries[idx+1:])
		n.entries[len(n.entries)-1] = mapEntry[K, V]{}
		n.entries = n.entries[:len(n.entries)-1]
		return n
	}

	// Otherwise create a copy with the given entry removed.
	other := &mapArrayNode[K, V]{entries: make([]mapEntry[K, V], len(n.entries)-1)}
	copy(other.entries[:idx], n.entries[:idx])
	copy(other.entries[idx:], n.entries[idx+1:])
	return other
}

// mapBitmapIndexedNode represents a map branch node with a variable number of
// node slots and indexed using a bitmap. Indexes for the node slots are
// calculated by counting the number of set bits before the target bit using popcount.
type mapBitmapIndexedNode[K comparable, V any] struct {
	bitmap uint32
	nodes  []mapNode[K, V]
}

// get returns the value for the given key.
func (n *mapBitmapIndexedNode[K, V]) get(key K, shift uint, keyHash uint32, h Hasher[K]) (value V, ok bool) {
	bit := uint32(1) << ((keyHash >> shift) & mapNodeMask)
	if (n.bitmap & bit) == 0 {
		return value, false
	}
	child := n.nodes[bits.OnesCount32(n.bitmap&(bit-1))]
	return child.get(key, shift+mapNodeBits, keyHash, h)
}

// set inserts or updates the value for the given key. If a new key is inserted
// and the size crosses the max size threshold then a hash array node is returned.
func (n *mapBitmapIndexedNode[K, V]) set(key K, value V, shift uint, keyHash uint32, h Hasher[K], mutable bool, resized *bool) mapNode[K, V] {
	// Extract the index for the bit segment of the key hash.
	keyHashFrag := (keyHash >> shift) & mapNodeMask

	// Determine the bit based on the hash index.
	bit := uint32(1) << keyHashFrag
	exists := (n.bitmap & bit) != 0

	// Mark as resized if the key doesn't exist.
	if !exists {
		*resized = true
	}

	// Find index of node based on popcount of bits before it.
	idx := bits.OnesCount32(n.bitmap & (bit - 1))

	// If the node already exists, delegate set operation to it.
	// If the node doesn't exist then create a simple value leaf node.
	var newNode mapNode[K, V]
	if exists {
		newNode = n.nodes[idx].set(key, value, shift+mapNodeBits, keyHash, h, mutable, resized)
	} else {
		newNode = newMapValueNode(keyHash, key, value)
	}

	// Convert to a hash-array node once we exceed the max bitmap size.
	// Copy each node based on their bit position within the bitmap.
	if !exists && len(n.nodes) > maxBitmapIndexedSize {
		var other mapHashArrayNode[K, V]
		for i := uint(0); i < uint(len(other.nodes)); i++ {
			if n.bitmap&(uint32(1)<<i) != 0 {
				other.nodes[i] = n.nodes[other.count]
				other.count++
			}
		}
		other.nodes[keyHashFrag] = newNode
		other.count++
		return &other
	}

	// Update in-place if mutable.
	if mutable {
		if exists {
			n.nodes[idx] = newNode
		} else {
			n.bitmap |= bit
			n.nodes = append(n.nodes, nil)
			copy(n.nodes[idx+1:], n.nodes[idx:])
			n.nodes[idx] = newNode
		}
		return n
	}

	// If node exists at given slot then overwrite it with new node.
	// Otherwise expand the node list and insert new node into appropriate position.
	other := &mapBitmapIndexedNode[K, V]{bitmap: n.bitmap | bit}
	if exists {
		other.nodes = make([]mapNode[K, V], len(n.nodes))
		copy(other.nodes, n.nodes)
		other.nodes[idx] = newNode
	} else {
		other.nodes = make([]mapNode[K, V], len(n.nodes)+1)
		copy(other.nodes, n.nodes[:idx])
		other.nodes[idx] = newNode
		copy(other.nodes[idx+1:], n.nodes[idx:])
	}
	return other
}

// delete removes the key from the tree. If the key does not exist then the
// original node is returned. If removing the last child node then a nil is
// returned. Note that shrinking the node will not convert it to an array node.
func (n *mapBitmapIndexedNode[K, V]) delete(key K, shift uint, keyHash uint32, h Hasher[K], mutable bool, resized *bool) mapNode[K, V] {
	bit := uint32(1) << ((keyHash >> shift) & mapNodeMask)

	// Return original node if key does not exist.
	if (n.bitmap & bit) == 0 {
		return n
	}

	// Find index of node based on popcount of bits before it.
	idx := bits.OnesCount32(n.bitmap & (bit - 1))

	// Delegate delete to child node.
	child := n.nodes[idx]
	newChild := child.delete(key, shift+mapNodeBits, keyHash, h, mutable, resized)

	// Return original node if key doesn't exist in child.
	if !*resized {
		return n
	}

	// Remove if returned child has been deleted.
	if newChild == nil {
		// If we won't have any children then return nil.
		if len(n.nodes) == 1 {
			return nil
		}

		// Update in-place if mutable.
		if mutable {
			n.bitmap ^= bit
			copy(n.nodes[idx:], n.nodes[idx+1:])
			n.nodes[len(n.nodes)-1] = nil
			n.nodes = n.nodes[:len(n.nodes)-1]
			return n
		}

		// Return copy with bit removed from bitmap and node removed from node list.
		other := &mapBitmapIndexedNode[K, V]{bitmap: n.bitmap ^ bit, nodes: make([]mapNode[K, V], len(n.nodes)-1)}
		copy(other.nodes[:idx], n.nodes[:idx])
		copy(other.nodes[idx:], n.nodes[idx+1:])
		return other
	}

	// Generate copy, if necessary.
	other := n
	if !mutable {
		other = &mapBitmapIndexedNode[K, V]{bitmap: n.bitmap, nodes: make([]mapNode[K, V], len(n.nodes))}
		copy(other.nodes, n.nodes)
	}

	// Update child.
	other.nodes[idx] = newChild
	return other
}

// mapHashArrayNode is a map branch node that stores nodes in a fixed length
// array. Child nodes are indexed by their index bit segment for the current depth.
type mapHashArrayNode[K comparable, V any] struct {
	count uint                       // number of set nodes
	nodes [mapNodeSize]mapNode[K, V] // child node slots, may contain empties
}

// clone returns a shallow copy of n.
func (n *mapHashArrayNode[K, V]) clone() *mapHashArrayNode[K, V] {
	other := *n
	return &other
}

// get returns the value for the given key.
func (n *mapHashArrayNode[K, V]) get(key K, shift uint, keyHash uint32, h Hasher[K]) (value V, ok bool) {
	node := n.nodes[(keyHash>>shift)&mapNodeMask]
	if node == nil {
		return value, false
	}
	return node.get(key, shift+mapNodeBits, keyHash, h)
}

// set returns a node with the value set for the given key.
func (n *mapHashArrayNode[K, V]) set(key K, value V, shift uint, keyHash uint32, h Hasher[K], mutable bool, resized *bool) mapNode[K, V] {
	idx := (keyHash >> shift) & mapNodeMask
	node := n.nodes[idx]

	// If node at index doesn't exist, create a simple value leaf node.
	// Otherwise delegate set to child node.
	var newNode mapNode[K, V]
	if node == nil {
		*resized = true
		newNode = newMapValueNode(keyHash, key, value)
	} else {
		newNode = node.set(key, value, shift+mapNodeBits, keyHash, h, mutable, resized)
	}

	// Generate copy, if necessary.
	other := n
	if !mutable {
		other = n.clone()
	}

	// Update child node (and update size, if new).
	if node == nil {
		other.count++
	}
	other.nodes[idx] = newNode
	return other
}

// delete returns a node with the given key removed. Returns the same node if
// the key does not exist. If node shrinks to within bitmap-indexed size then
// converts to a bitmap-indexed node.
func (n *mapHashArrayNode[K, V]) delete(key K, shift uint, keyHash uint32, h Hasher[K], mutable bool, resized *bool) mapNode[K, V] {
	idx := (keyHash >> shift) & mapNodeMask
	node := n.nodes[idx]

	// Return original node if child is not found.
	if node == nil {
		return n
	}

	// Return original node if child is unchanged.
	newNode := node.delete(key, shift+mapNodeBits, keyHash, h, mutable, resized)
	if !*resized {
		return n
	}

	// If we remove a node and drop below a threshold, convert back to bitmap indexed node.
	if newNode == nil && n.count <= maxBitmapIndexedSize {
		other := &mapBitmapIndexedNode[K, V]{nodes: make([]mapNode[K, V], 0, n.count-1)}
		for i, child := range n.nodes {
			if child != nil && uint32(i) != idx {
				other.bitmap |= 1 << uint(i)
				other.nodes = append(other.nodes, child)
			}
		}
		return other
	}

	// Generate copy, if necessary.
	other := n
	if !mutable {
		other = n.clone()
	}

	// Return copy of node with child updated.
	other.nodes[idx] = newNode
	if newNode == nil {
		other.count--
	}
	return other
}

// mapValueNode represents a leaf node with a single key/value pair.
// A value node can be converted to a hash collision leaf node if a different
// key with the same keyHash is inserted.
type mapValueNode[K comparable, V any] struct {
	keyHash uint32
	key     K
	value   V
}

// newMapValueNode returns a new instance of mapValueNode.
func newMapValueNode[K comparable, V any](keyHash uint32, key K, value V) *mapValueNode[K, V] {
	return &mapValueNode[K, V]{
		keyHash: keyHash,
		key:     key,
		value:   value,
	}
}

// keyHashValue returns the key hash for this node.
func (n *mapValueNode[K, V]) keyHashValue() uint32 {
	return n.keyHash
}

// get returns the value for the given key.
func (n *mapValueNode[K, V]) get(key K, shift uint, keyHash uint32, h Hasher[K]) (value V, ok bool) {
	if !h.Equal(n.key, key) {
		return value, false
	}
	return n.value, true
}

// set returns a new node with the new value set for the key. If the key equals
// the node's key then a new value node is returned. If key is not equal to the
// node's key but has the same hash then a hash collision node is returned.
// Otherwise the nodes are merged into a branch node.
func (n *mapValueNode[K, V]) set(key K, value V, shift uint, keyHash uint32, h Hasher[K], mutable bool, resized *bool) mapNode[K, V] {
	// If the keys match then return a new value node overwriting the value.
	if h.Equal(n.key, key) {
		// Update in-place if mutable.
		if mutable {
			n.value = value
			return n
		}
		// Otherwise return a new copy.
		return newMapValueNode(n.keyHash, key, value)
	}

	*resized = true

	// Recursively merge nodes together if key hashes are different.
	if n.keyHash != keyHash {
		return mergeIntoNode[K, V](n, shift, keyHash, key, value)
	}

	// Merge into collision node if hash matches.
	return &mapHashCollisionNode[K, V]{keyHash: keyHash, entries: []mapEntry[K, V]{
		{key: n.key, value: n.value},
		{key: key, value: value},
	}}
}

// delete returns nil if the key matches the node's key. Otherwise returns the original node.
func (n *mapValueNode[K, V]) delete(key K, shift uint, keyHash uint32, h Hasher[K], mutable bool, resized *bool) mapNode[K, V] {
	// Return original node if the keys do not match.
	if !h.Equal(n.key, key) {
		return n
	}

	// Otherwise remove the node if keys do match.
	*resized = true
	return nil
}

// mapHashCollisionNode represents a leaf node that contains two or more key/value
// pairs with the same key hash. Single pairs for a hash are stored as value nodes.
type mapHashCollisionNode[K comparable, V any] struct {
	keyHash uint32 // key hash for all entries
	entries []mapEntry[K, V]
}

// keyHashValue returns the key hash for all entries on the node.
func (n *mapHashCollisionNode[K, V]) keyHashValue() uint32 {
	return n.keyHash
}

// indexOf returns the index of the entry for the given key.
// Returns -1 if the key does not exist in the node.
func (n *mapHashCollisionNode[K, V]) indexOf(key K, h Hasher[K]) int {
	for i := range n.entries {
		if h.Equal(n.entries[i].key, key) {
			return i
		}
	}
	return -1
}

// get returns the value for the given key.
func (n *mapHashCollisionNode[K, V]) get(key K, shift uint, keyHash uint32, h Hasher[K]) (value V, ok bool) {
	for i := range n.entries {
		if h.Equal(n.entries[i].key, key) {
			return n.entries[i].value, true
		}
	}
	return value, false
}

// set returns a copy of the node with key set to the given value.
func (n *mapHashCollisionNode[K, V]) set(key K, value V, shift uint, keyHash uint32, h Hasher[K], mutable bool, resized *bool) mapNode[K, V] {
	// Merge node with key/value pair if this is not a hash collision.
	if n.keyHash != keyHash {
		*resized = true
		return mergeIntoNode[K, V](n, shift, keyHash, key, value)
	}

	// Update in-place if mutable.
	if mutable {
		if idx := n.indexOf(key, h); idx == -1 {
			*resized = true
			n.entries = append(n.entries, mapEntry[K, V]{key, value})
		} else {
			n.entries[idx] = mapEntry[K, V]{key, value}
		}
		return n
	}

	// Append to end of node if key doesn't exist & mark resized.
	// Otherwise copy nodes and overwrite at matching key index.
	other := &mapHashCollisionNode[K, V]{keyHash: n.keyHash}
	if idx := n.indexOf(key, h); idx == -1 {
		*resized = true
		other.entries = make([]mapEntry[K, V], len(n.entries)+1)
		copy(other.entries, n.entries)
		other.entries[len(other.entries)-1] = mapEntry[K, V]{key, value}
	} else {
		other.entries = make([]mapEntry[K, V], len(n.entries))
		copy(other.entries, n.entries)
		other.entries[idx] = mapEntry[K, V]{key, value}
	}
	return other
}

// delete returns a node with the given key deleted. Returns the same node if
// the key does not exist. If removing the key would shrink the node to a single
// entry then a value node is returned.
func (n *mapHashCollisionNode[K, V]) delete(key K, shift uint, keyHash uint32, h Hasher[K], mutable bool, resized *bool) mapNode[K, V] {
	idx := n.indexOf(key, h)

	// Return original node if key is not found.
	if idx == -1 {
		return n
	}

	// Mark as resized if key exists.
	*resized = true

	// Convert to value node if we move to one entry.
	if len(n.entries) == 2 {
		return &mapValueNode[K, V]{
			keyHash: n.keyHash,
			key:     n.entries[idx^1].key,
			value:   n.entries[idx^1].value,
		}
	}

	// Remove entry in-place if mutable.
	if mutable {
		copy(n.entries[idx:], n.entries[idx+1:])
		n.entries[len(n.entries)-1] = mapEntry[K, V]{}
		n.entries = n.entries[:len(n.entries)-1]
		return n
	}

	// Return copy without entry if immutable.
	other := &mapHashCollisionNode[K, V]{keyHash: n.keyHash, entries: make([]mapEntry[K, V], len(n.entries)-1)}
	copy(other.entries[:idx], n.entries[:idx])
	copy(other.entries[idx:], n.entries[idx+1:])
	return other
}

// mergeIntoNode merges a key/value pair into an existing node.
// Caller must verify that node's keyHash is not equal to keyHash.
func mergeIntoNode[K comparable, V any](node mapLeafNode[K, V], shift uint, keyHash uint32, key K, value V) mapNode[K, V] {
	idx1 := (node.keyHashValue() >> shift) & mapNodeMask
	idx2 := (keyHash >> shift) & mapNodeMask

	// Recursively build branch nodes to combine the node and its key.
	other := &mapBitmapIndexedNode[K, V]{bitmap: (1 << idx1) | (1 << idx2)}
	if idx1 == idx2 {
		other.nodes = []mapNode[K, V]{mergeIntoNode(node, shift+mapNodeBits, keyHash, key, value)}
	} else {
		if newNode := newMapValueNode(keyHash, key, value); idx1 < idx2 {
			other.nodes = []mapNode[K, V]{node, newNode}
		} else {
			other.nodes = []mapNode[K, V]{newNode, node}
		}
	}
	return other
}

// mapEntry represents a single key/value pair.
type mapEntry[K comparable, V any] struct {
	key   K
	value V
}

// MapIterator represents an iterator over a map's key/value pairs. Although
// map keys are not sorted, the iterator's order is deterministic.
type MapIterator[K comparable, V any] struct {
	m *Map[K, V] // source map

	stack [32]mapIteratorElem[K, V] // search stack
	depth int                       // stack depth
}

// Done returns true if no more elements remain in the iterator.
func (itr *MapIterator[K, V]) Done() bool {
	return itr.depth == -1
}

// First resets the iterator to the first key/value pair.
func (itr *MapIterator[K, V]) First() {
	// Exit immediately if the map is empty.
	if itr.m.root == nil {
		itr.depth = -1
		return
	}

	// Initialize the stack to the left most element.
	itr.stack[0] = mapIteratorElem[K, V]{node: itr.m.root}
	itr.depth = 0
	itr.first()
}

// Next returns the next key/value pair. Returns a nil key when no elements remain.
func (itr *MapIterator[K, V]) Next() (key K, value V, ok bool) {
	// Return nil key if iteration is done.
	if itr.Done() {
		return key, value, false
	}

	// Retrieve current index & value. Current node is always a leaf.
	elem := &itr.stack[itr.depth]
	switch node := elem.node.(type) {
	case *mapArrayNode[K, V]:
		entry := &node.entries[elem.index]
		key, value = entry.key, entry.value
	case *mapValueNode[K, V]:
		key, value = node.key, node.value
	case *mapHashCollisionNode[K, V]:
		entry := &node.entries[elem.index]
		key, value = entry.key, entry.value
	}

	// Move up stack until we find a node that has remaining position ahead
	// and move that element forward by one.
	itr.next()
	return key, value, true
}

// next moves to the next available key.
func (itr *MapIterator[K, V]) next() {
	for ; itr.depth >= 0; itr.depth-- {
		elem := &itr.stack[itr.depth]

		switch node := elem.node.(type) {
		case *mapArrayNode[K, V]:
			if elem.index < len(node.entries)-1 {
				elem.index++
				return
			}

		case *mapBitmapIndexedNode[K, V]:
			if elem.index < len(node.nodes)-1 {
				elem.index++
				itr.stack[itr.depth+1].node = node.nodes[elem.index]
				itr.depth++
				itr.first()
				return
			}

		case *mapHashArrayNode[K, V]:
			for i := elem.index + 1; i < len(node.nodes); i++ {
				if node.nodes[i] != nil {
					elem.index = i
					itr.stack[itr.depth+1].node = node.nodes[elem.index]
					itr.depth++
					itr.first()
					return
				}
			}

		case *mapValueNode[K, V]:
			continue // always the last value, traverse up

		case *mapHashCollisionNode[K, V]:
			if elem.index < len(node.entries)-1 {
				elem.index++
				return
			}
		}
	}
}

// first positions the stack left most index.
// Elements and indexes at and below the current depth are assumed to be correct.
func (itr *MapIterator[K, V]) first() {
	for ; ; itr.depth++ {
		elem := &itr.stack[itr.depth]

		switch node := elem.node.(type) {
		case *mapBitmapIndexedNode[K, V]:
			elem.index = 0
			itr.stack[itr.depth+1].node = node.nodes[0]

		case *mapHashArrayNode[K, V]:
			for i := 0; i < len(node.nodes); i++ {
				if node.nodes[i] != nil { // find first node
					elem.index = i
					itr.stack[itr.depth+1].node = node.nodes[i]
					break
				}
			}

		default: // *mapArrayNode, mapLeafNode
			elem.index = 0
			return
		}
	}
}

// mapIteratorElem represents a node/index pair in the MapIterator stack.
type mapIteratorElem[K comparable, V any] struct {
	node  mapNode[K, V]
	index int
}

// Hasher hashes keys and checks them for equality.
type Hasher[K comparable] interface {
	// Computes a hash for key.
	Hash(key K) uint32

	// Returns true if a and b are equal.
	Equal(a, b K) bool
}

// NewHasher returns the built-in hasher for a given key type.
func NewHasher[K comparable](key K) Hasher[K] {
	// Attempt to use non-reflection based hasher first.
	switch (any(key)).(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, uintptr, string:
		return &defaultHasher[K]{}
	}

	// Fallback to reflection-based hasher otherwise.
	// This is used when caller wraps a type around a primitive type.
	switch reflect.TypeOf(key).Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr, reflect.String:
		return &reflectHasher[K]{}
	}

	// If no hashers match then panic.
	// This is a compile time issue so it should not return an error.
	panic(fmt.Sprintf("immutable.NewHasher: must set hasher for %T type", key))
}

// Hash returns a hash for value.
func hashString(value string) uint32 {
	var hash uint32
	for i, value := 0, value; i < len(value); i++ {
		hash = 31*hash + uint32(value[i])
	}
	return hash
}

// reflectIntHasher implements a reflection-based Hasher for int keys.
type reflectHasher[K comparable] struct{}

// Hash returns a hash for key.
func (h *reflectHasher[K]) Hash(key K) uint32 {
	switch reflect.TypeOf(key).Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return hashUint64(uint64(reflect.ValueOf(key).Int()))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return hashUint64(reflect.ValueOf(key).Uint())
	case reflect.String:
		var hash uint32
		s := reflect.ValueOf(key).String()
		for i := 0; i < len(s); i++ {
			hash = 31*hash + uint32(s[i])
		}
		return hash
	}
	panic(fmt.Sprintf("immutable.reflectHasher.Hash: reflectHasher does not support %T type", key))
}

// Equal returns true if a is equal to b. Otherwise returns false.
// Panics if a and b are not ints.
func (h *reflectHasher[K]) Equal(a, b K) bool {
	switch reflect.TypeOf(a).Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return reflect.ValueOf(a).Int() == reflect.ValueOf(b).Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return reflect.ValueOf(a).Uint() == reflect.ValueOf(b).Uint()
	case reflect.String:
		return reflect.ValueOf(a).String() == reflect.ValueOf(b).String()
	}
	panic(fmt.Sprintf("immutable.reflectHasher.Equal: reflectHasher does not support %T type", a))

}

// hashUint64 returns a 32-bit hash for a 64-bit value.
func hashUint64(value uint64) uint32 {
	hash := value
	for value > 0xffffffff {
		value /= 0xffffffff
		hash ^= value
	}
	return uint32(hash)
}

// defaultHasher implements Hasher.
type defaultHasher[K comparable] struct{}

// Hash returns a hash for key.
func (h *defaultHasher[K]) Hash(key K) uint32 {
	// Attempt to use non-reflection based hasher first.
	switch x := (any(key)).(type) {
	case int:
		return hashUint64(uint64(x))
	case int8:
		return hashUint64(uint64(x))
	case int16:
		return hashUint64(uint64(x))
	case int32:
		return hashUint64(uint64(x))
	case int64:
		return hashUint64(uint64(x))
	case uint:
		return hashUint64(uint64(x))
	case uint8:
		return hashUint64(uint64(x))
	case uint16:
		return hashUint64(uint64(x))
	case uint32:
		return hashUint64(uint64(x))
	case uint64:
		return hashUint64(uint64(x))
	case uintptr:
		return hashUint64(uint64(x))
	case string:
		return hashString(x)
	}
	panic(fmt.Sprintf("immutable.defaultHasher.Hash: must set comparer for %T type", key))
}

// Equal returns true if a is equal to b. Otherwise returns false.
// Panics if a and b are not ints.
func (h *defaultHasher[K]) Equal(a, b K) bool {
	return a == b
}

func assert(condition bool, message string) {
	if !condition {
		panic(message)
	}
}
