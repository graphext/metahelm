[![Go Documentation](http://img.shields.io/badge/go-documentation-blue.svg?style=flat-square)][godocs]

[godocs]: https://godoc.org/github.com/dollarshaveclub/metahelm

[![CircleCI](https://circleci.com/gh/dollarshaveclub/metahelm.svg?style=svg&circle-token=f906b4f6996b06331f7872e99bd2eb8d26bee537)](https://circleci.com/gh/dollarshaveclub/metahelm)

# Metahelm

Metahelm is a CLI and library for installing dependency graphs of Helm charts.

## Why?

With respect, Helm dependency handling...isn't great. It doesn't handle complex
trees of dependencies very well; it simply creates all the Kubernetes objects
essentially at once and waits for them all to settle into a consistent state.

Unfortunately, with a complex set of dependency relationships, this often means
one or more applications go into CrashLoopBackoff, greatly lengthening the time for
the entire tree to come up. Sometimes they will completely fail when one or
more applications give up entirely.

## What does Metahelm do?

Metahelm takes a list of charts (as YAML) with dependency relationships specified,
and optionally a way for Metahelm to tell when a chart is "up" or successful.

Metahelm will build a dependency graph (DAG) of the charts and install them in
order such that all dependencies are satisfied prior to each chart install.

## Sample YAML

Let's say you have a primary application ("YOLO") that depends upon several
microservices to be running for the primary app to start. Let's call those microservices
"Alpha", "Bravo" and "Charlie". Alpha depends upon a MySQL database, Bravo needs
PostgreSQL and all three need Redis.

An example YAML input might look like the following:

```yaml
- name: YOLO
  path: /home/charts/yolo
  values_path: /home/releases/yolo/values.yml
  primary_deployment: yolo
  dependencies:
    - alpha
    - bravo
    - charlie
- name: alpha
  path: /home/charts/alpha
  values_path: /home/releases/alpha/values.yml
  primary_deployment: alpha
  wait_for_all_pods: true
  dependencies:
    - mysql
    - redis
- name: bravo
  path: /home/charts/bravo
  values_path: /home/releases/bravo/values.yml
  primary_deployment: bravo
  wait_for_all_pods: true
  dependencies:
    - postgres
    - redis
- name: charlie
  path: /home/charts/charlie
  values_path: /home/releases/charlie/values.yml
  primary_deployment: charlie
  wait_for_all_pods: true
  dependencies:
    - redis
- name: mysql
  path: /home/charts/mysql
  values_path: /home/releases/mysql/values.yml
  primary_deployment: mysql
- name: postgres
  path: /home/charts/postgres
  values_path: /home/releases/postgres/values.yml
  primary_deployment: postgres
- name: redis
  path: /home/charts/redis
  values_path: /home/releases/redis/values.yml
  primary_deployment: redis
```
