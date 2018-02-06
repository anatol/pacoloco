#ifndef __LIST_H_
#define __LIST_H_

#include <stddef.h>

// CONTAINER

#define container_of(ptr, type, member)                    \
    ({                                                     \
        const typeof(((type *)0)->member) *__mptr = (ptr); \
        (type *)((char *)__mptr - offsetof(type, member)); \
    })

// LIST

struct list_head {
    struct list_head *next, *prev;
};

#define LIST_HEAD_INIT(name) \
    { &(name), &(name) }

#define LIST_HEAD(name) struct list_head name = LIST_HEAD_INIT(name)

static inline void INIT_LIST_HEAD(struct list_head *list) {
    list->next = list;
    list->prev = list;
}

static inline void __list_add(struct list_head *new, struct list_head *prev, struct list_head *next) {
    next->prev = new;
    new->next = next;
    new->prev = prev;
    prev->next = new;
}

static inline void list_add(struct list_head *head, struct list_head *new) { __list_add(new, head, head->next); }

static inline void list_add_tail(struct list_head *head, struct list_head *new) { __list_add(new, head->prev, head); }

static inline void list_del(struct list_head *node) {
    node->next->prev = node->prev;
    node->prev->next = node->next;

    node->prev = NULL;
    node->next = NULL;
}

static inline int list_empty(const struct list_head *head) { return head->next == head; }

#define list_entry(ptr, type, member) container_of(ptr, type, member)

#define list_first_entry(ptr, type, member) list_entry((ptr)->next, type, member)

#define list_next_entry(pos, member) list_entry((pos)->member.next, typeof(*(pos)), member)

#define list_for_each(pos, head, member) \
    for (pos = list_first_entry(head, typeof(*pos), member); &pos->member != (head); pos = list_next_entry(pos, member))

#define list_for_each_safe(pos, n, head, member)                                                                       \
    for (pos = list_first_entry(head, typeof(*pos), member), n = list_next_entry(pos, member); &pos->member != (head); \
         pos = n, n = list_next_entry(n, member))

#endif // __LIST_H_
