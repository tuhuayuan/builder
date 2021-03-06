package gitreceive

import (
	"fmt"
	"time"

	"github.com/deis/builder/pkg/k8s"
	"github.com/pborman/uuid"
	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/labels"
	"k8s.io/kubernetes/pkg/util/wait"
)

const (
	slugBuilderName   = "deis-slugbuilder"
	dockerBuilderName = "deis-dockerbuilder"

	tarPath          = "TAR_PATH"
	putPath          = "PUT_PATH"
	debugKey         = "DEIS_DEBUG"
	objectStore      = "objectstorage-keyfile"
	dockerSocketName = "docker-socket"
	dockerSocketPath = "/var/run/docker.sock"
	builderStorage   = "BUILDER_STORAGE"
	objectStorePath  = "/var/run/secrets/deis/objectstore/creds"
)

func dockerBuilderPodName(appName, shortSha string) string {
	uid := uuid.New()[:8]
	return fmt.Sprintf("dockerbuild-%s-%s-%s", appName, shortSha, uid)
}

func slugBuilderPodName(appName, shortSha string) string {
	uid := uuid.New()[:8]
	return fmt.Sprintf("slugbuild-%s-%s-%s", appName, shortSha, uid)
}

func dockerBuilderPod(
	debug bool,
	name,
	namespace string,
	env map[string]interface{},
	tarKey,
	imageName,
	storageType,
	image,
	registryHost,
	registryPort string,
	registryEnv map[string]string,
	pullPolicy api.PullPolicy,
) *api.Pod {

	pod := buildPod(debug, name, namespace, pullPolicy, env)

	pod.Spec.Containers[0].Name = dockerBuilderName
	pod.Spec.Containers[0].Image = image

	addEnvToPod(pod, tarPath, tarKey)
	addEnvToPod(pod, "IMG_NAME", imageName)
	addEnvToPod(pod, builderStorage, storageType)
	// inject existing DEIS_REGISTRY_SERVICE_HOST and PORT info to dockerbuilder
	// see https://github.com/deis/dockerbuilder/issues/83
	addEnvToPod(pod, "DEIS_REGISTRY_SERVICE_HOST", registryHost)
	addEnvToPod(pod, "DEIS_REGISTRY_SERVICE_PORT", registryPort)

	for key, value := range registryEnv {
		addEnvToPod(pod, key, value)
	}

	pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, api.VolumeMount{
		Name:      dockerSocketName,
		MountPath: dockerSocketPath,
	})

	pod.Spec.Volumes = append(pod.Spec.Volumes, api.Volume{
		Name: dockerSocketName,
		VolumeSource: api.VolumeSource{
			HostPath: &api.HostPathVolumeSource{
				Path: dockerSocketPath,
			},
		},
	})

	return &pod
}

func slugbuilderPod(
	debug bool,
	name,
	namespace string,
	env map[string]interface{},
	tarKey,
	putKey,
	buildpackURL,
	storageType,
	image string,
	pullPolicy api.PullPolicy,
) *api.Pod {

	pod := buildPod(debug, name, namespace, pullPolicy, env)

	pod.Spec.Containers[0].Name = slugBuilderName
	pod.Spec.Containers[0].Image = image

	addEnvToPod(pod, tarPath, tarKey)
	addEnvToPod(pod, putPath, putKey)
	addEnvToPod(pod, builderStorage, storageType)

	if buildpackURL != "" {
		addEnvToPod(pod, "BUILDPACK_URL", buildpackURL)
	}

	return &pod
}

func buildPod(
	debug bool,
	name,
	namespace string,
	pullPolicy api.PullPolicy,
	env map[string]interface{}) api.Pod {

	pod := api.Pod{
		Spec: api.PodSpec{
			RestartPolicy: api.RestartPolicyNever,
			Containers: []api.Container{
				{
					ImagePullPolicy: pullPolicy,
				},
			},
			Volumes: []api.Volume{},
		},
		ObjectMeta: api.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"heritage": name,
			},
		},
	}

	pod.Spec.Volumes = append(pod.Spec.Volumes, api.Volume{
		Name: objectStore,
		VolumeSource: api.VolumeSource{
			Secret: &api.SecretVolumeSource{
				SecretName: objectStore,
			},
		},
	})

	pod.Spec.Containers[0].VolumeMounts = []api.VolumeMount{
		{
			Name:      objectStore,
			MountPath: objectStorePath,
			ReadOnly:  true,
		},
	}

	if len(pod.Spec.Containers) > 0 {
		for k, v := range env {
			pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, api.EnvVar{
				Name:  k,
				Value: fmt.Sprintf("%v", v),
			})
		}
	}

	if debug {
		addEnvToPod(pod, debugKey, "1")
	}

	return pod
}

func addEnvToPod(pod api.Pod, key, value string) {
	if len(pod.Spec.Containers) > 0 {
		pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, api.EnvVar{
			Name:  key,
			Value: value,
		})
	}
}

// waitForPod waits for a pod in state running, succeeded or failed
func waitForPod(pw *k8s.PodWatcher, ns, podName string, ticker, interval, timeout time.Duration) error {
	condition := func(pod *api.Pod) (bool, error) {
		if pod.Status.Phase == api.PodRunning {
			return true, nil
		}
		if pod.Status.Phase == api.PodSucceeded {
			return true, nil
		}
		if pod.Status.Phase == api.PodFailed {
			return true, fmt.Errorf("Giving up; pod went into failed status: \n%s", fmt.Sprintf("%#v", pod))
		}
		return false, nil
	}

	quit := progress("...", ticker)
	err := waitForPodCondition(pw, ns, podName, condition, interval, timeout)
	quit <- true
	<-quit
	return err
}

// waitForPodEnd waits for a pod in state succeeded or failed
func waitForPodEnd(pw *k8s.PodWatcher, ns, podName string, interval, timeout time.Duration) error {
	condition := func(pod *api.Pod) (bool, error) {
		if pod.Status.Phase == api.PodSucceeded {
			return true, nil
		}
		if pod.Status.Phase == api.PodFailed {
			return true, nil
		}
		return false, nil
	}

	return waitForPodCondition(pw, ns, podName, condition, interval, timeout)
}

// waitForPodCondition waits for a pod in state defined by a condition (func)
func waitForPodCondition(pw *k8s.PodWatcher, ns, podName string, condition func(pod *api.Pod) (bool, error),
	interval, timeout time.Duration) error {
	return wait.PollImmediate(interval, timeout, func() (bool, error) {
		pods, err := pw.Store.List(labels.Set{"heritage": podName}.AsSelector())
		if err != nil || len(pods) == 0 {
			return false, nil
		}

		done, err := condition(pods[0])
		if err != nil {
			return false, err
		}
		if done {
			return true, nil
		}

		return false, nil
	})
}

func progress(msg string, interval time.Duration) chan bool {
	tick := time.Tick(interval)
	quit := make(chan bool)
	go func() {
		for {
			select {
			case <-quit:
				close(quit)
				return
			case <-tick:
				fmt.Println(msg)
			}
		}
	}()
	return quit
}
